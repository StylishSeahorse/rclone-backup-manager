from sqlalchemy import Column, Integer, String, Boolean, DateTime, Text, ForeignKey
from sqlalchemy.ext.declarative import declarative_base
from datetime import datetime

Base = declarative_base()

class User(Base):
    __tablename__ = "users"
    id = Column(Integer, primary_key=True)
    username = Column(String, unique=True, nullable=False)
    hashed_password = Column(String, nullable=False)
    created_at = Column(DateTime, default=datetime.utcnow)
class RemoteProfile(Base):
    __tablename__ = "remote_profiles"
    id = Column(Integer, primary_key=True)
    name = Column(String, unique=True, nullable=False)
    remote_type = Column(String, nullable=False)  # s3, gdrive
    encrypted_credentials = Column(Text, nullable=False)
    # S3 specific
    region = Column(String, nullable=True)
    endpoint = Column(String, nullable=True)
    created_at = Column(DateTime, default=datetime.utcnow)
    updated_at = Column(DateTime, default=datetime.utcnow, onupdate=datetime.utcnow)
class Agent(Base):
    __tablename__ = "agents"
    id = Column(Integer, primary_key=True)
    hostname = Column(String, nullable=False)
    ip_address = Column(String, nullable=False)
    platform = Column(String, nullable=False)  # linux, windows, macos
    version = Column(String, default="1.0.0")
    status = Column(String, default="offline")  # online, offline
    agent_token = Column(String, unique=True, nullable=False)
    last_seen = Column(DateTime, default=datetime.utcnow)
    created_at = Column(DateTime, default=datetime.utcnow)

class BackupConfig(Base):
    __tablename__ = "backup_configs"
    id = Column(Integer, primary_key=True)
    name = Column(String, unique=True, nullable=False)
    agent_id = Column(Integer, ForeignKey('agents.id'), nullable=True)  # NULL = local/server
    source_path = Column(String, nullable=False)
    remote_type = Column(String, nullable=False)
    remote_name = Column(String, nullable=False)
    remote_path = Column(String, nullable=False)
    encrypted_credentials = Column(Text, nullable=False)
    is_incremental = Column(Boolean, default=True)
    schedule_cron = Column(String, default="0 2 * * *")
    enabled = Column(Boolean, default=True)
    keep_daily_days = Column(Integer, default=3)
    keep_weekly = Column(Boolean, default=True)
    last_run = Column(DateTime, nullable=True)
    created_at = Column(DateTime, default=datetime.utcnow)
    updated_at = Column(DateTime, default=datetime.utcnow, onupdate=datetime.utcnow)

class BackupJob(Base):
    __tablename__ = "backup_jobs"
    id = Column(Integer, primary_key=True)
    config_id = Column(Integer, ForeignKey('backup_configs.id'), nullable=False)
    agent_id = Column(Integer, ForeignKey('agents.id'), nullable=True)
    status = Column(String, default="pending")
    started_at = Column(DateTime, default=datetime.utcnow)
    completed_at = Column(DateTime, nullable=True)
    log_file = Column(String, nullable=True)
    error_message = Column(Text, nullable=True)
    bytes_transferred = Column(Integer, default=0)