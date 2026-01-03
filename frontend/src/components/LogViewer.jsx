import React, { useState, useEffect } from 'react';
import axios from 'axios';

function LogViewer({ jobId, token, onClose }) {
  const [logs, setLogs] = useState('');

  useEffect(() => {
    const api = axios.create({ baseURL: 'http://localhost:8000/api' });
    api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
    
    api.get(`/jobs/${jobId}/logs`).then(res => {
      setLogs(res.data.logs);
    });
  }, [jobId]);

  return (
    <div style={styles.overlay}>
      <div style={styles.modal}>
        <h3>Job {jobId} Logs</h3>
        <pre style={styles.logs}>{logs}</pre>
        <button onClick={onClose} style={styles.btn}>Close</button>
      </div>
    </div>
  );
}

const styles = {
  overlay: { position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.7)', display: 'flex', justifyContent: 'center', alignItems: 'center', zIndex: 1000 },
  modal: { background: 'white', padding: '30px', borderRadius: '8px', maxWidth: '800px', width: '90%', maxHeight: '80vh', overflow: 'auto' },
  logs: { background: '#2c3e50', color: '#ecf0f1', padding: '15px', borderRadius: '5px', overflow: 'auto', maxHeight: '400px', fontSize: '12px' },
  btn: { marginTop: '15px', background: '#3498db', color: 'white', padding: '10px 20px', border: 'none', borderRadius: '5px', cursor: 'pointer' }
};

export default LogViewer;