from apscheduler.schedulers.background import BackgroundScheduler
from apscheduler.triggers.cron import CronTrigger
from sqlalchemy.orm import Session
from .models import BackupConfig
from .database import SessionLocal
import asyncio

class BackupScheduler:
    def __init__(self, rclone_manager):
        self.scheduler = BackgroundScheduler()
        self.rclone_manager = rclone_manager
    
    def start(self):
        # Load all enabled configs
        db = SessionLocal()
        configs = db.query(BackupConfig).filter(BackupConfig.enabled == True).all()
        for config in configs:
            self.add_job(config)
        db.close()
        
        self.scheduler.start()
    
    def stop(self):
        self.scheduler.shutdown()
    
    def add_job(self, config: BackupConfig):
        # Parse cron (e.g., "0 2 * * *")
        parts = config.schedule_cron.split()
        trigger = CronTrigger(
            minute=parts[0],
            hour=parts[1],
            day=parts[2],
            month=parts[3],
            day_of_week=parts[4]
        )
        
        self.scheduler.add_job(
            self._run_backup_sync,
            trigger=trigger,
            id=f"backup_{config.id}",
            args=[config.id],
            replace_existing=True
        )
    
    def remove_job(self, config_id: int):
        try:
            self.scheduler.remove_job(f"backup_{config_id}")
        except:
            pass
    
    def _run_backup_sync(self, config_id: int):
        """Sync wrapper for async backup"""
        db = SessionLocal()
        config = db.query(BackupConfig).filter(BackupConfig.id == config_id).first()
        if config:
            asyncio.run(self.rclone_manager.run_backup(config, db))
        db.close()