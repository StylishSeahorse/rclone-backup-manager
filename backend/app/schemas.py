from pydantic import BaseModel, Field
from typing import Optional, Dict
from datetime import datetime

class UserCreate(BaseModel):
    username: str
    password: str

class Token(BaseModel):
    access_token: str
    token_type: str

class AgentRegister(BaseModel):
    hostname: str
    ip_address: str
    platform: str
    version: str = "1.0.0"

class AgentResponse(BaseModel):
    id: int
    hostname: str
    ip_address: str
    platform: str
    version: str
    status: str
    last_seen: Optional[datetime]
    created_at: datetime
    
    class Config:
        from_attributes = True
class AgentRegisterResponse(BaseModel):
    id: int
    hostname: str
    ip_address: str
    platform: str
    version: str
    status: str
    agent_token: str  # Only included during registration
    last_seen: Optional[datetime]
    created_at: datetime
    
    class Config:
        from_attributes = True
class AgentTokenResponse(BaseModel):
    token: str
    expires_in: int = 3600

class BackupConfigCreate(BaseModel):
    name: str
    agent_id: Optional[int] = None
    source_path: str
    remote_type: str
    remote_name: str
    remote_path: str
    credentials: Dict[str, str]
    is_incremental: bool = True
    schedule_cron: str = "0 2 * * *"
    enabled: bool = True
    keep_daily_days: int = 3
    keep_weekly: bool = True

class BackupConfigResponse(BaseModel):
    id: int
    name: str
    agent_id: Optional[int]
    source_path: str
    remote_type: str
    remote_name: str
    remote_path: str
    is_incremental: bool
    schedule_cron: str
    enabled: bool
    last_run: Optional[datetime]
    
    class Config:
        from_attributes = True

class BackupJobResponse(BaseModel):
    id: int
    config_id: int
    agent_id: Optional[int]
    status: str
    started_at: datetime
    completed_at: Optional[datetime]
    error_message: Optional[str]
    bytes_transferred: int
    
    class Config:
        from_attributes = True
class RemoteProfileCreate(BaseModel):
    name: str
    remote_type: str
    credentials: Dict[str, str]
    region: Optional[str] = "us-east-1"
    endpoint: Optional[str] = "s3.wasabisys.com"

class RemoteProfileResponse(BaseModel):
    id: int
    name: str
    remote_type: str
    region: Optional[str]
    endpoint: Optional[str]
    created_at: datetime
    
    class Config:
        from_attributes = True

# Update BackupConfigCreate to support profile_id
class BackupConfigCreate(BaseModel):
    name: str
    agent_id: Optional[int] = None
    source_path: str
    remote_profile_id: Optional[int] = None  # NEW: use saved profile
    remote_type: Optional[str] = None  # Optional if using profile
    remote_name: str
    remote_path: str
    credentials: Optional[Dict[str, str]] = None  # Optional if using profile
    is_incremental: bool = True
    schedule_cron: str = "0 2 * * *"
    enabled: bool = True
    keep_daily_days: int = 3
    keep_weekly: bool = True        