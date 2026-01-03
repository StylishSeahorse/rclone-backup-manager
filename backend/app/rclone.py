import subprocess
import json
import os
from datetime import datetime
from cryptography.fernet import Fernet
from sqlalchemy.orm import Session
from .models import BackupJob, BackupConfig

class RcloneManager:
    def __init__(self):
        key_file = "/app/data/encryption.key"
        os.makedirs("/app/data", exist_ok=True)
        
        if os.path.exists(key_file):
            with open(key_file, 'rb') as f:
                self.key = f.read()
        else:
            self.key = Fernet.generate_key()
            with open(key_file, 'wb') as f:
                f.write(self.key)
        
        self.cipher = Fernet(self.key)
        self.config_dir = "/app/data/rclone"
        os.makedirs(self.config_dir, exist_ok=True)
    
    def encrypt_credentials(self, creds: dict) -> str:
        return self.cipher.encrypt(json.dumps(creds).encode()).decode()
    
    def decrypt_credentials(self, encrypted: str) -> dict:
        return json.loads(self.cipher.decrypt(encrypted.encode()).decode())
    
    def create_remote(self, config: BackupConfig):
        creds = self.decrypt_credentials(config.encrypted_credentials)
        
        if config.remote_type == "s3":
            conf = f"""[{config.remote_name}]
type = s3
provider = Wasabi
access_key_id = {creds['access_key']}
secret_access_key = {creds['secret_key']}
region = {creds.get('region', 'us-east-1')}
endpoint = {creds.get('endpoint', 's3.wasabisys.com')}
acl = private
"""
        elif config.remote_type == "gdrive":
            conf = f"""[{config.remote_name}]
type = drive
client_id = {creds['client_id']}
client_secret = {creds['client_secret']}
token = {creds['token']}
"""
        else:
            raise ValueError(f"Unsupported remote type: {config.remote_type}")
        
        config_file = f"{self.config_dir}/rclone.conf"
        
        if os.path.exists(config_file):
            with open(config_file, 'r') as f:
                existing = f.read()
            lines = []
            skip = False
            for line in existing.split('\n'):
                if line.strip() == f"[{config.remote_name}]":
                    skip = True
                elif line.startswith('['):
                    skip = False
                if not skip:
                    lines.append(line)
            existing = '\n'.join(lines)
        else:
            existing = ""
        
        with open(config_file, 'w') as f:
            f.write(existing + '\n' + conf)
    
    def delete_remote(self, remote_name: str):
        config_file = f"{self.config_dir}/rclone.conf"
        if not os.path.exists(config_file):
            return
        
        with open(config_file, 'r') as f:
            lines = f.readlines()
        
        new_lines = []
        skip = False
        for line in lines:
            if line.strip() == f"[{remote_name}]":
                skip = True
            elif line.startswith('['):
                skip = False
            if not skip:
                new_lines.append(line)
        
        with open(config_file, 'w') as f:
            f.writelines(new_lines)
    
    async def run_backup(self, config: BackupConfig, db: Session) -> BackupJob:
        """Execute backup job (local or via agent)"""
        from .models import Agent
        
        # Get agent if specified
        agent = None
        if config.agent_id:
            agent = db.query(Agent).filter(Agent.id == config.agent_id).first()
            if not agent:
                raise Exception(f"Agent not found: {config.agent_id}")
            if agent.status != "online":
                raise Exception(f"Agent is offline: {agent.hostname}")
        
        job = BackupJob(
            config_id=config.id,
            agent_id=config.agent_id,
            status="pending" if agent else "running",  # Pending if using agent, running if local
            started_at=datetime.utcnow()
        )
        db.add(job)
        db.commit()
        db.refresh(job)
        
        log_file = f"/app/data/logs/job_{job.id}.log"
        os.makedirs("/app/data/logs", exist_ok=True)
        job.log_file = log_file
        db.commit()
        
        # If using agent, job stays pending - agent will pick it up
        if agent:
            return job
        
        # Otherwise execute locally (existing code)
        try:
            remote = f"{config.remote_name}:{config.remote_path}"
            date_str = datetime.now().strftime('%Y-%m-%d_%H-%M-%S')
            
            cmd = [
                "rclone", "sync",
                config.source_path,
                remote,
                "--progress",
                "--s3-no-ac",l
                "--s3-no-check-bucket",
                "--s3-no-head",
                "--config", f"{self.config_dir}/rclone.conf",
                "--log-file", log_file,
                "--log-level", "INFO",
                "--stats", "5s",
                "--transfers", "8",
                "--checkers", "16"
            ]
            
            if config.is_incremental:
                safe_job_name = config.name.replace(' ', '-').replace('/', '-').lower()
                backup_dir = f"{config.remote_name}:BACKUPS/{config.id}-{safe_job_name}/{date_str}"
                cmd.extend(["--backup-dir", backup_dir])
            
            result = subprocess.run(cmd, capture_output=True, text=True)
            
            bytes_transferred = 0
            if os.path.exists(log_file):
                with open(log_file, 'r') as f:
                    for line in f:
                        if "Transferred:" in line:
                            try:
                                parts = line.split()
                                if "Bytes" in line or "bytes" in line:
                                    bytes_transferred = int(parts[1].replace(',', ''))
                            except:
                                pass
            
            if result.returncode == 0:
                job.status = "success"
                job.bytes_transferred = bytes_transferred
                
                if config.is_incremental and config.keep_weekly:
                    await self._prune_backups(config)
            else:
                job.status = "failed"
                job.error_message = result.stderr
            
            job.completed_at = datetime.utcnow()
            config.last_run = datetime.utcnow()
            db.commit()
            
        except Exception as e:
            job.status = "failed"
            job.error_message = str(e)
            job.completed_at = datetime.utcnow()
            db.commit()
        
        return job
    
    async def _execute_on_agent(self, agent, cmd, log_file):
        """Execute command on remote agent"""
        # For now, execute locally (full agent support in next phase)
        result = subprocess.run(cmd, capture_output=True, text=True)
        return result
    
    async def _prune_backups(self, config: BackupConfig):
        """Prune old backups"""
        safe_job_name = config.name.replace(' ', '-').replace('/', '-').lower()
        remote = f"{config.remote_name}:BACKUPS/{config.id}-{safe_job_name}"
    
    # ... rest stays the same
        
        if len(path_parts) > 1:
            backup_parent = '/'.join(path_parts) + '-backups'
        else:
            backup_parent = f"{bucket}/backups"
        
        remote = f"{config.remote_name}:{backup_parent}"
        
        cmd = [
            "rclone", "lsf", remote,
            "--config", f"{self.config_dir}/rclone.conf",
            "--dirs-only"
        ]
        
        result = subprocess.run(cmd, capture_output=True, text=True)
        if result.returncode != 0:
            return
        
        dirs = [d.strip('/') for d in result.stdout.strip().split('\n') if d]
        dirs.sort(reverse=True)
        
        keep_count = config.keep_daily_days + (1 if config.keep_weekly else 0)
        to_delete = dirs[keep_count:]
        
        for old_dir in to_delete:
            del_cmd = [
                "rclone", "purge",
                f"{remote}/{old_dir}",
                "--config", f"{self.config_dir}/rclone.conf"
            ]
            subprocess.run(del_cmd)