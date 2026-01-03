from fastapi.middleware.cors import CORSMiddleware
from sqlalchemy.orm import Session
import asyncio
import os
import secrets
from datetime import datetime, timedelta
from .models import Agent
from .schemas import AgentRegister, AgentResponse, AgentTokenResponse
from fastapi import Request
from fastapi import FastAPI, Depends, HTTPException, WebSocket, WebSocketDisconnect, Body
from typing import List, Dict, Any

from .database import engine, get_db, init_db
from .models import Base, BackupConfig, BackupJob
from .schemas import *
from .auth import get_current_user, create_access_token, verify_password, get_password_hash
from .rclone import RcloneManager
from .scheduler import BackupScheduler
from .models import RemoteProfile
from .schemas import RemoteProfileCreate, RemoteProfileResponse


def validate_origin(origin: str) -> bool:
    """
    Validate if origin should be allowed.
    Returns True to allow any origin, or add custom logic here.
    """
    # Allow all origins
    return True
    
# Initialize
Base.metadata.create_all(bind=engine)
app = FastAPI(title="Rclone Backup Manager")

# CORS
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],  # Allow all origins when behind nginx proxy
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# Globals
rclone_manager = RcloneManager()
scheduler = BackupScheduler(rclone_manager)
# Remote Profile endpoints
@app.post("/api/profiles", response_model=RemoteProfileResponse)
def create_profile(
    profile: RemoteProfileCreate,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    """Save a remote credential profile"""
    encrypted_creds = rclone_manager.encrypt_credentials(profile.credentials)
    
    db_profile = RemoteProfile(
        name=profile.name,
        remote_type=profile.remote_type,
        encrypted_credentials=encrypted_creds,
        region=profile.region,
        endpoint=profile.endpoint
    )
    
    db.add(db_profile)
    db.commit()
    db.refresh(db_profile)
    
    return db_profile

@app.get("/api/profiles", response_model=List[RemoteProfileResponse])
def list_profiles(
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    """List all saved remote profiles"""
    return db.query(RemoteProfile).all()

@app.get("/api/profiles/{profile_id}", response_model=RemoteProfileResponse)
def get_profile(
    profile_id: int,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    """Get a specific profile (without showing credentials)"""
    profile = db.query(RemoteProfile).filter(RemoteProfile.id == profile_id).first()
    if not profile:
        raise HTTPException(status_code=404, detail="Profile not found")
    return profile

@app.delete("/api/profiles/{profile_id}")
def delete_profile(
    profile_id: int,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    """Delete a remote profile"""
    profile = db.query(RemoteProfile).filter(RemoteProfile.id == profile_id).first()
    if not profile:
        raise HTTPException(status_code=404, detail="Profile not found")
    
    # Check if any configs use this profile
    configs = db.query(BackupConfig).filter(BackupConfig.remote_type == profile.remote_type).all()
    # Note: We don't have profile_id FK yet, so just warn
    
    db.delete(profile)
    db.commit()
    return {"detail": "Profile deleted"}
# Agent management endpoints
@app.post("/api/agents/generate-token", response_model=AgentTokenResponse)
def generate_agent_token(current_user: str = Depends(get_current_user)):
    """Generate a one-time installation token"""
    token = secrets.token_urlsafe(32)
    # Store token with expiry (in production, use Redis)
    # For now, we'll validate in register endpoint with timestamp check
    return {"token": token, "expires_in": 3600}

@app.post("/api/agents/register", response_model=AgentRegisterResponse)  # ← Changed from AgentResponse
def register_agent(agent: AgentRegister, install_token: str, db: Session = Depends(get_db)):
    """Agent self-registration endpoint"""
    
    # Check if agent already exists by hostname
    existing = db.query(Agent).filter(Agent.hostname == agent.hostname).first()
    if existing:
        # Update existing agent
        existing.ip_address = agent.ip_address
        existing.platform = agent.platform
        existing.version = agent.version
        existing.status = "online"
        existing.last_seen = datetime.utcnow()
        db.commit()
        db.refresh(existing)
        return existing  # This now includes agent_token
    
    # Create new agent
    agent_token = secrets.token_urlsafe(32)
    new_agent = Agent(
        hostname=agent.hostname,
        ip_address=agent.ip_address,
        platform=agent.platform,
        version=agent.version,
        agent_token=agent_token,
        status="online",
        last_seen=datetime.utcnow()
    )
    db.add(new_agent)
    db.commit()
    db.refresh(new_agent)
    
    return new_agent

@app.get("/api/agents", response_model=List[AgentResponse])
def list_agents(
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    """List all registered agents"""
    agents = db.query(Agent).all()
    
    # Update status based on last_seen (>5 minutes = offline)
    for agent in agents:
        if agent.last_seen:
            delta = datetime.utcnow() - agent.last_seen
            if delta.total_seconds() > 300:  # 5 minutes
                agent.status = "offline"
            else:
                agent.status = "online"
    
    db.commit()
    return agents

@app.post("/api/agents/{agent_id}/heartbeat")
def agent_heartbeat(agent_id: int, agent_token: str, db: Session = Depends(get_db)):
    """Agent heartbeat to update status"""
    agent = db.query(Agent).filter(
        Agent.id == agent_id,
        Agent.agent_token == agent_token
    ).first()
    
    if not agent:
        raise HTTPException(status_code=401, detail="Invalid agent credentials")
    
    agent.last_seen = datetime.utcnow()
    agent.status = "online"
    db.commit()
    
    return {"status": "ok"}

@app.delete("/api/agents/{agent_id}")
def delete_agent(
    agent_id: int,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    """Remove an agent"""
    agent = db.query(Agent).filter(Agent.id == agent_id).first()
    if not agent:
        raise HTTPException(status_code=404, detail="Agent not found")
    
    # Check if any configs use this agent
    configs = db.query(BackupConfig).filter(BackupConfig.agent_id == agent_id).all()
    if configs:
        raise HTTPException(
            status_code=400, 
            detail=f"Cannot delete agent with {len(configs)} active backup jobs"
        )
    
    db.delete(agent)
    db.commit()
    return {"detail": "Agent deleted"}

@app.get("/api/agents/install.sh")
def get_install_script():
    """Return complete agent installation script with worker"""
    from fastapi.responses import PlainTextResponse
    
    script = '''#!/bin/bash
set -e

TOKEN="$1"
SERVER_URL="${2:-http://$(ip route get 1 | awk '{print $7;exit}'):8000}"

if [ -z "$TOKEN" ]; then
    echo "Usage: $0 <install-token> [server-url]"
    exit 1
fi

echo "Installing Rclone Backup Agent..."

# Detect platform
PLATFORM=$(uname -s | tr '[:upper:]' '[:lower:]')
HOSTNAME=$(hostname)
IP_ADDRESS=$(hostname -I | awk '{print $1}' || echo "127.0.0.1")

# Install dependencies
if command -v apt-get &> /dev/null; then
    apt-get update
    apt-get install -y rclone curl jq
elif command -v yum &> /dev/null; then
    yum install -y epel-release
    yum install -y rclone curl jq
else
    echo "Unsupported package manager. Install rclone, curl, jq manually."
    exit 1
fi

# Create agent directory
mkdir -p /opt/rclone-agent/logs
cd /opt/rclone-agent

# Register agent
echo "Registering agent with server..."
RESPONSE=$(curl -s -X POST "$SERVER_URL/api/agents/register?install_token=$TOKEN" \\
    -H "Content-Type: application/json" \\
    -d "{\\"hostname\\":\\"$HOSTNAME\\",\\"ip_address\\":\\"$IP_ADDRESS\\",\\"platform\\":\\"$PLATFORM\\",\\"version\\":\\"1.0.0\\"}")

AGENT_ID=$(echo $RESPONSE | jq -r '.id')
AGENT_TOKEN=$(echo $RESPONSE | jq -r '.agent_token')

if [ "$AGENT_ID" = "null" ] || [ -z "$AGENT_ID" ] || [ "$AGENT_TOKEN" = "null" ]; then
    echo "Failed to register agent:"
    echo $RESPONSE
    exit 1
fi

# Save config
cat > /opt/rclone-agent/config.json <<EOF
{
    "agent_id": $AGENT_ID,
    "agent_token": "$AGENT_TOKEN",
    "server_url": "$SERVER_URL"
}
EOF

# Create agent worker script
cat > /opt/rclone-agent/agent-worker.sh <<'WORKER'
#!/bin/bash
set -e

CONFIG_FILE="/opt/rclone-agent/config.json"
AGENT_ID=$(jq -r '.agent_id' $CONFIG_FILE)
AGENT_TOKEN=$(jq -r '.agent_token' $CONFIG_FILE)
SERVER_URL=$(jq -r '.server_url' $CONFIG_FILE)

echo "Rclone Agent Worker Started (Agent ID: $AGENT_ID)"
mkdir -p /opt/rclone-agent/logs

while true; do
    # Heartbeat
    curl -s -X POST "$SERVER_URL/api/agents/$AGENT_ID/heartbeat?agent_token=$AGENT_TOKEN" > /dev/null 2>&1 || true
    
    # Get pending jobs
    JOBS=$(curl -s -X GET "$SERVER_URL/api/agents/$AGENT_ID/jobs?agent_token=$AGENT_TOKEN" 2>/dev/null || echo '[]')
    
    # Process each job
    echo "$JOBS" | jq -c '.[]' 2>/dev/null | while read -r job; do
        JOB_ID=$(echo "$job" | jq -r '.id')
        SOURCE=$(echo "$job" | jq -r '.source_path')
        REMOTE=$(echo "$job" | jq -r '.remote')
        BACKUP_DIR=$(echo "$job" | jq -r '.backup_dir // empty')
        RCLONE_CONFIG=$(echo "$job" | jq -r '.rclone_config')
        
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] Processing job $JOB_ID: $SOURCE -> $REMOTE"
        
        CONFIG_PATH="/tmp/rclone-$JOB_ID.conf"
        echo "$RCLONE_CONFIG" > "$CONFIG_PATH"
        
        LOG_FILE="/opt/rclone-agent/logs/job_$JOB_ID.log"
        RCLONE_CMD="rclone sync \\"$SOURCE\\" \\"$REMOTE\\" --config \\"$CONFIG_PATH\\" --log-file \\"$LOG_FILE\\" --log-level INFO --stats 30s --transfers 8"
        
        if [ -n "$BACKUP_DIR" ]; then
            RCLONE_CMD="$RCLONE_CMD --backup-dir \\"$BACKUP_DIR\\""
        fi
        
        if eval $RCLONE_CMD >> "$LOG_FILE" 2>&1; then
            STATUS="success"
            ERROR_MSG=""
        else
            STATUS="failed"
            ERROR_MSG="Rclone command failed"
        fi
        
        BYTES=$(grep "Transferred:" "$LOG_FILE" 2>/dev/null | tail -1 | grep -oP '\\d+(?= )' | head -1 || echo "0")
        
        # Build JSON properly with jq
        REPORT_JSON=$(jq -n \\
            --arg status "$STATUS" \\
            --arg error "$ERROR_MSG" \\
            --arg bytes "${BYTES:-0}" \\
            '{status: $status, error: $error, bytes_transferred: ($bytes | tonumber)}')
        
        # Report completion
        curl -s -X POST "$SERVER_URL/api/agents/$AGENT_ID/jobs/$JOB_ID/complete?agent_token=$AGENT_TOKEN" \\
            -H "Content-Type: application/json" \\
            -d "$REPORT_JSON" > /dev/null 2>&1
        
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] Job $JOB_ID completed: $STATUS"
        rm -f "$CONFIG_PATH"
    done
    
    sleep 30
done
WORKER

chmod +x /opt/rclone-agent/agent-worker.sh

# Create systemd service
cat > /etc/systemd/system/rclone-agent.service <<EOF
[Unit]
Description=Rclone Backup Agent Worker
After=network.target

[Service]
Type=simple
ExecStart=/opt/rclone-agent/agent-worker.sh
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

# Start service
systemctl daemon-reload
systemctl enable rclone-agent
systemctl start rclone-agent

echo "✓ Agent installed successfully!"
echo "  Agent ID: $AGENT_ID"
echo "  Hostname: $HOSTNAME"
echo "  Status: systemctl status rclone-agent"
echo ""
echo "View logs: sudo journalctl -u rclone-agent -f"
'''
    
    return PlainTextResponse(content=script, media_type="text/plain")

@app.get("/api/agents/{agent_id}/paths")
def list_agent_paths(
    agent_id: int,
    path: str = "/",
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    """List directories on agent (for path selection in UI)"""
    agent = db.query(Agent).filter(Agent.id == agent_id).first()
    if not agent:
        raise HTTPException(status_code=404, detail="Agent not found")
    
    # In production, send request to agent via WebSocket/HTTP
    # For now, return mock data
    return {
        "path": path,
        "directories": [
            "/home",
            "/var/www",
            "/opt",
            "/data"
        ]
    }

@app.on_event("startup")
async def startup():
    init_db()
    scheduler.start()

@app.on_event("shutdown")
async def shutdown():
    scheduler.stop()

# Auth endpoints
@app.post("/api/auth/register", response_model=Token)
def register(user: UserCreate, db: Session = Depends(get_db)):
    print(f"Registration attempt: username={user.username}")
    
    from .models import User
    existing = db.query(User).filter(User.username == user.username).first()
    if existing:
        print(f"Username {user.username} already exists")
        raise HTTPException(status_code=400, detail="Username exists")
    
    new_user = User(
        username=user.username,
        hashed_password=get_password_hash(user.password)
    )
    db.add(new_user)
    db.commit()
    
    token = create_access_token(data={"sub": user.username})
    print(f"User {user.username} created successfully")
    return {"access_token": token, "token_type": "bearer"}

@app.post("/api/auth/login", response_model=Token)
def login(user: UserCreate, db: Session = Depends(get_db)):
    from .models import User
    db_user = db.query(User).filter(User.username == user.username).first()
    if not db_user or not verify_password(user.password, db_user.hashed_password):
        raise HTTPException(status_code=401, detail="Invalid credentials")
    
    token = create_access_token(data={"sub": user.username})
    return {"access_token": token, "token_type": "bearer"}

# Backup config endpoints
@app.post("/api/configs", response_model=BackupConfigResponse)
def create_config(
    config: BackupConfigCreate,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    # Use profile or direct credentials
    if config.remote_profile_id:
        profile = db.query(RemoteProfile).filter(RemoteProfile.id == config.remote_profile_id).first()
        if not profile:
            raise HTTPException(status_code=404, detail="Remote profile not found")
        
        encrypted_creds = profile.encrypted_credentials
        remote_type = profile.remote_type
    else:
        if not config.credentials:
            raise HTTPException(status_code=400, detail="Credentials or profile required")
        encrypted_creds = rclone_manager.encrypt_credentials(config.credentials)
        remote_type = config.remote_type
    
    db_config = BackupConfig(
        name=config.name,
        agent_id=config.agent_id,
        source_path=config.source_path,
        remote_type=remote_type,
        remote_name=config.remote_name,
        remote_path=config.remote_path,
        encrypted_credentials=encrypted_creds,
        is_incremental=config.is_incremental,
        schedule_cron=config.schedule_cron,
        enabled=config.enabled,
        keep_daily_days=config.keep_daily_days,
        keep_weekly=config.keep_weekly
    )
    
    db.add(db_config)
    db.commit()
    db.refresh(db_config)
    
    rclone_manager.create_remote(db_config)
    
    if config.enabled:
        scheduler.add_job(db_config)
    
    return db_config

@app.get("/api/configs", response_model=List[BackupConfigResponse])
def list_configs(
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    return db.query(BackupConfig).all()

@app.get("/api/agents/{agent_id}/jobs")
def get_agent_jobs(
    agent_id: int,
    agent_token: str,
    db: Session = Depends(get_db)
):
    """Get pending backup jobs for an agent"""
    from datetime import timedelta
    
    agent = db.query(Agent).filter(
        Agent.id == agent_id,
        Agent.agent_token == agent_token
    ).first()
    
    if not agent:
        raise HTTPException(status_code=401, detail="Invalid agent credentials")
    
    # Find pending jobs OR running jobs that started >2 minutes ago (might have failed)
    cutoff = datetime.utcnow() - timedelta(minutes=2)
    
    pending_jobs = db.query(BackupJob).filter(
        BackupJob.agent_id == agent_id,
        (
            (BackupJob.status == "pending") |
            ((BackupJob.status == "running") & (BackupJob.started_at < cutoff))
        )
    ).all()
    
    jobs_data = []
    for job in pending_jobs:
        config = db.query(BackupConfig).filter(BackupConfig.id == job.config_id).first()
        if config:
            # Decrypt credentials
            creds = rclone_manager.decrypt_credentials(config.encrypted_credentials)
            
            # Generate rclone config
            if config.remote_type == "s3":
                rclone_conf = f"""[{config.remote_name}]
type = s3
provider = Wasabi
access_key_id = {creds['access_key']}
secret_access_key = {creds['secret_key']}
region = {creds.get('region', 'us-east-1')}
endpoint = {creds.get('endpoint', 's3.wasabisys.com')}
"""
            else:
                continue
            
            # Build backup-dir
            backup_dir = None
            if config.is_incremental:
                # Use a completely separate backup prefix to avoid any overlap
                # Format: remote:BACKUPS/config-id/date
                safe_job_name = config.name.replace(' ', '-').replace('/', '-').lower()
                date_str = datetime.now().strftime('%Y-%m-%d_%H-%M-%S')
                
                # Put backups at remote root level with BACKUPS prefix
                # This guarantees no overlap with any data path
                backup_dir = f"{config.remote_name}:BACKUPS/{config.id}-{safe_job_name}/{date_str}"
            
            jobs_data.append({
                "id": job.id,
                "source_path": config.source_path,
                "remote": f"{config.remote_name}:{config.remote_path}",
                "backup_dir": backup_dir,
                "rclone_config": rclone_conf
            })
            
            # Update status to running (only if it was pending)
            if job.status == "pending":
                job.status = "running"
    
    db.commit()
    
    print(f"Returning {len(jobs_data)} jobs to agent {agent_id}")
    
    return jobs_data

from typing import Dict, Any

@app.post("/api/agents/{agent_id}/jobs/{job_id}/complete")
def complete_agent_job(
    agent_id: int,
    job_id: int,
    agent_token: str,
    payload: Dict[str, Any] = Body(...),  # Accept any JSON body
    db: Session = Depends(get_db)
):
    """Agent reports job completion"""
    agent = db.query(Agent).filter(
        Agent.id == agent_id,
        Agent.agent_token == agent_token
    ).first()
    
    if not agent:
        raise HTTPException(status_code=401, detail="Invalid agent credentials")
    
    job = db.query(BackupJob).filter(BackupJob.id == job_id).first()
    if not job:
        raise HTTPException(status_code=404, detail="Job not found")
    
    print(f"Job {job_id} completion received: {payload}")
    
    # Update job
    job.status = payload.get('status', 'failed')
    job.completed_at = datetime.utcnow()
    
    bytes_val = payload.get('bytes_transferred', 0)
    try:
        job.bytes_transferred = int(bytes_val) if bytes_val else 0
    except:
        job.bytes_transferred = 0
    
    if payload.get('error'):
        job.error_message = str(payload.get('error'))
    
    # Update config last_run
    config = db.query(BackupConfig).filter(BackupConfig.id == job.config_id).first()
    if config:
        config.last_run = datetime.utcnow()
    
    db.commit()
    
    print(f"✓ Job {job_id} marked as {job.status}, {job.bytes_transferred} bytes")
    
    return {"status": "ok", "message": f"Job {job_id} completed"}
@app.post("/api/jobs/reset-stuck")
def reset_stuck_jobs(
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    """Reset only jobs stuck in pending/running for > 5 minutes"""
    from datetime import timedelta
    
    cutoff = datetime.utcnow() - timedelta(minutes=5)
    
    stuck = db.query(BackupJob).filter(
        BackupJob.status.in_(["pending", "running"]),
        BackupJob.started_at < cutoff,
        BackupJob.completed_at.is_(None)
    ).all()
    
    count = len(stuck)
    
    for job in stuck:
        job.status = "failed"
        job.error_message = "Job stuck/timed out - reset by admin"
        job.completed_at = datetime.utcnow()
    
    db.commit()
    
    return {"detail": f"Reset {count} stuck jobs (pending/running > 5 min)"}
@app.delete("/api/jobs/{job_id}")
def cancel_job(
    job_id: int,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    """Cancel/delete a stuck job"""
    job = db.query(BackupJob).filter(BackupJob.id == job_id).first()
    if not job:
        raise HTTPException(status_code=404, detail="Job not found")
    
    if job.status in ["pending", "running"]:
        job.status = "cancelled"
        job.completed_at = datetime.utcnow()
        job.error_message = "Cancelled by user"
        db.commit()
    else:
        # Delete completed jobs
        db.delete(job)
        db.commit()
    
    return {"detail": "Job cancelled"}

@app.put("/api/configs/{config_id}", response_model=BackupConfigResponse)
def update_config(
    config_id: int,
    config: BackupConfigCreate,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    db_config = db.query(BackupConfig).filter(BackupConfig.id == config_id).first()
    if not db_config:
        raise HTTPException(status_code=404, detail="Config not found")
    
    # Update basic fields
    db_config.name = config.name
    db_config.agent_id = config.agent_id
    db_config.source_path = config.source_path
    db_config.remote_name = config.remote_name
    db_config.remote_path = config.remote_path
    db_config.is_incremental = config.is_incremental
    db_config.schedule_cron = config.schedule_cron
    db_config.enabled = config.enabled
    db_config.keep_daily_days = config.keep_daily_days
    db_config.keep_weekly = config.keep_weekly
    
    # Update credentials only if provided
    if config.remote_profile_id:
        # Using profile
        profile = db.query(RemoteProfile).filter(RemoteProfile.id == config.remote_profile_id).first()
        if profile:
            db_config.encrypted_credentials = profile.encrypted_credentials
            db_config.remote_type = profile.remote_type
    elif config.credentials and (config.credentials.get('access_key') or config.credentials.get('secret_key')):
        # New credentials provided - update them
        db_config.encrypted_credentials = rclone_manager.encrypt_credentials(config.credentials)
        db_config.remote_type = config.remote_type
    # else: keep existing encrypted_credentials (user left fields blank)
    
    db.commit()
    db.refresh(db_config)
    
    # Update rclone remote
    rclone_manager.create_remote(db_config)
    
    # Reschedule
    scheduler.remove_job(config_id)
    if db_config.enabled:
        scheduler.add_job(db_config)
    
    return db_config

@app.delete("/api/configs/{config_id}")
def delete_config(
    config_id: int,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    db_config = db.query(BackupConfig).filter(BackupConfig.id == config_id).first()
    if not db_config:
        raise HTTPException(status_code=404, detail="Config not found")
    
    scheduler.remove_job(config_id)
    rclone_manager.delete_remote(db_config.remote_name)
    db.delete(db_config)
    db.commit()
    
    return {"detail": "Deleted"}

# Job endpoints
@app.post("/api/configs/{config_id}/run")
async def run_backup(
    config_id: int,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    db_config = db.query(BackupConfig).filter(BackupConfig.id == config_id).first()
    if not db_config:
        raise HTTPException(status_code=404, detail="Config not found")
    
    job = await rclone_manager.run_backup(db_config, db)
    return {"job_id": job.id, "status": job.status}

@app.get("/api/jobs", response_model=List[BackupJobResponse])
def list_jobs(
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    return db.query(BackupJob).order_by(BackupJob.started_at.desc()).limit(50).all()

@app.get("/api/jobs/{job_id}/logs")
async def get_logs(
    job_id: int,
    db: Session = Depends(get_db),
    current_user: str = Depends(get_current_user)
):
    job = db.query(BackupJob).filter(BackupJob.id == job_id).first()
    if not job or not job.log_file:
        raise HTTPException(status_code=404, detail="Log not found")
    
    if not os.path.exists(job.log_file):
        return {"logs": "Log file not found"}
    
    with open(job.log_file, 'r') as f:
        return {"logs": f.read()}

# WebSocket for live logs
@app.websocket("/ws/logs/{job_id}")
async def websocket_logs(websocket: WebSocket, job_id: int):
    await websocket.accept()
    db = next(get_db())
    job = db.query(BackupJob).filter(BackupJob.id == job_id).first()
    
    if not job:
        await websocket.close(code=1008)
        return
    
    try:
        # Tail log file
        if job.log_file and os.path.exists(job.log_file):
            with open(job.log_file, 'r') as f:
                f.seek(0, 2)  # End
                while job.status == "running":
                    line = f.readline()
                    if line:
                        await websocket.send_text(line)
                    else:
                        await asyncio.sleep(0.5)
                        db.refresh(job)
    except WebSocketDisconnect:
        pass