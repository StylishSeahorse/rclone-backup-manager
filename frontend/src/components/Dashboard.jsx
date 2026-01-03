import React, { useState, useEffect } from 'react';
import axios from 'axios';
import ConfigForm from './ConfigForm';
import LogViewer from './LogViewer';
import AgentManager from './AgentManager';

const api = axios.create({
  baseURL: '/api'
});

function Dashboard({ token, onLogout }) {
  const [configs, setConfigs] = useState([]);
  const [jobs, setJobs] = useState([]);
  const [agents, setAgents] = useState([]);
  const [showForm, setShowForm] = useState(false);
  const [selectedJob, setSelectedJob] = useState(null);
  const [activeTab, setActiveTab] = useState('configs');
  const [loading, setLoading] = useState(true);
  const [editConfig, setEditConfig] = useState(null);
  const resetStuckJobs = async () => {
  if (confirm('Reset all stuck jobs?')) {
    try {
      await api.post('/jobs/reset-stuck');
      loadJobs();
    } catch (err) {
      alert('Failed: ' + (err.response?.data?.detail || err.message));
    }
  }
};

  useEffect(() => {
    api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
    loadData();
    const interval = setInterval(() => {
      loadJobs();
      loadAgents();
    }, 10000);
    return () => clearInterval(interval);
  }, []);

  const loadData = async () => {
    await Promise.all([loadConfigs(), loadJobs(), loadAgents()]);
    setLoading(false);
  };

  const loadConfigs = async () => {
    try {
      const res = await api.get('/configs');
      setConfigs(res.data);
    } catch (err) {
      console.error('Failed to load configs:', err);
    }
  };

  const loadJobs = async () => {
    try {
      const res = await api.get('/jobs');
      setJobs(res.data);
    } catch (err) {
      console.error('Failed to load jobs:', err);
    }
  };

  const loadAgents = async () => {
    try {
      const res = await api.get('/agents');
      setAgents(res.data);
    } catch (err) {
      console.error('Failed to load agents:', err);
    }
  };

  const runBackup = async (configId) => {
    try {
      await api.post(`/configs/${configId}/run`);
      setTimeout(loadJobs, 1000);
    } catch (err) {
      alert('Failed to start backup: ' + (err.response?.data?.detail || err.message));
    }
  };

  const deleteConfig = async (id) => {
    if (confirm('Delete this backup config?')) {
      try {
        await api.delete(`/configs/${id}`);
        loadConfigs();
      } catch (err) {
        alert('Failed to delete: ' + (err.response?.data?.detail || err.message));
      }
    }
  };
  const cancelJob = async (jobId) => {
    if (confirm('Cancel this job?')) {
      try {
        await api.delete(`/jobs/${jobId}`);
        loadJobs();
      } catch (err) {
        alert('Failed to cancel: ' + (err.response?.data?.detail || err.message));
      }
    }
  };
  const getStatusColor = (status) => {
    const colors = {
      success: '#10b981',
      failed: '#ef4444',
      running: '#f59e0b',
      pending: '#6b7280',
      online: '#10b981',
      offline: '#ef4444'
    };
    return colors[status] || '#6b7280';
  };

  if (loading) return (
    <div style={styles.loadingContainer}>
      <div style={styles.spinner}></div>
      <p style={styles.loadingText}>Loading dashboard...</p>
    </div>
  );

  return (
    <div style={styles.container}>
      <header style={styles.header}>
        <div style={styles.headerLeft}>
          <div style={styles.logo}>
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
              <polyline points="3.27 6.96 12 12.01 20.73 6.96"/>
              <line x1="12" y1="22.08" x2="12" y2="12"/>
            </svg>
          </div>
          <h1 style={styles.title}>Rclone Backup Manager</h1>
        </div>
        <div style={styles.headerRight}>
          <div style={styles.stats}>
            <div style={styles.statItem}>
              <span style={styles.statValue}>{agents.filter(a => a.status === 'online').length}</span>
              <span style={styles.statLabel}>Agents Online</span>
            </div>
            <div style={styles.statItem}>
              <span style={styles.statValue}>{configs.length}</span>
              <span style={styles.statLabel}>Backup Jobs</span>
            </div>
          </div>
          <button onClick={onLogout} style={styles.logoutBtn}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/>
              <polyline points="16 17 21 12 16 7"/>
              <line x1="21" y1="12" x2="9" y2="12"/>
            </svg>
            Logout
          </button>
        </div>
      </header>

      <nav style={styles.tabs}>
        <button 
          style={{...styles.tab, ...(activeTab === 'agents' ? styles.tabActive : {})}}
          onClick={() => setActiveTab('agents')}
        >
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <rect x="2" y="3" width="20" height="14" rx="2" ry="2"/>
            <line x1="8" y1="21" x2="16" y2="21"/>
            <line x1="12" y1="17" x2="12" y2="21"/>
          </svg>
          Agents
        </button>
        <button 
          style={{...styles.tab, ...(activeTab === 'configs' ? styles.tabActive : {})}}
          onClick={() => setActiveTab('configs')}
        >
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
          </svg>
          Backup Jobs
        </button>
        <button 
          style={{...styles.tab, ...(activeTab === 'history' ? styles.tabActive : {})}}
          onClick={() => setActiveTab('history')}
        >
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <circle cx="12" cy="12" r="10"/>
            <polyline points="12 6 12 12 16 14"/>
          </svg>
          History
        </button>
      </nav>

      <main style={styles.main}>
        {activeTab === 'agents' && (
          <AgentManager agents={agents} onRefresh={loadAgents} token={token} />
        )}

        {activeTab === 'configs' && (
          <section style={styles.section}>
            <div style={styles.sectionHeader}>
              <h2 style={styles.sectionTitle}>Backup Configurations</h2>
              <button onClick={() => setShowForm(true)} style={styles.primaryBtn}>
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <line x1="12" y1="5" x2="12" y2="19"/>
                  <line x1="5" y1="12" x2="19" y2="12"/>
                </svg>
                New Backup Job
              </button>
            </div>
            
            {showForm && (
              <ConfigForm
                token={token}
                agents={agents.filter(a => a.status === 'online')}
                onSave={() => { setShowForm(false); setEditConfig(null); loadConfigs(); }}
                onCancel={() => { setShowForm(false); setEditConfig(null); }}
                editConfig={editConfig}
              />
            )}

            {configs.length === 0 ? (
              <div style={styles.emptyState}>
                <svg width="64" height="64" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
                </svg>
                <h3>No backup jobs configured</h3>
                <p>Create your first backup job to get started</p>
              </div>
            ) : (
              <div style={styles.grid}>
                {configs.map(cfg => (
                  <div key={cfg.id} style={styles.card}>
                    <div style={styles.cardHeader}>
                      <h3 style={styles.cardTitle}>{cfg.name}</h3>
                      <span style={{...styles.badge, background: cfg.enabled ? '#10b981' : '#6b7280'}}>
                        {cfg.enabled ? 'Active' : 'Disabled'}
                      </span>
                    </div>
                    <div style={styles.cardBody}>
                      <div style={styles.infoRow}>
                        <span style={styles.infoLabel}>Source:</span>
                        <code style={styles.infoValue}>{cfg.source_path}</code>
                      </div>
                      <div style={styles.infoRow}>
                        <span style={styles.infoLabel}>Destination:</span>
                        <code style={styles.infoValue}>{cfg.remote_name}:{cfg.remote_path}</code>
                      </div>
                      <div style={styles.infoRow}>
                        <span style={styles.infoLabel}>Type:</span>
                        <span style={styles.infoValue}>{cfg.is_incremental ? 'Incremental' : 'Full'}</span>
                      </div>
                      <div style={styles.infoRow}>
                        <span style={styles.infoLabel}>Schedule:</span>
                        <code style={styles.infoValue}>{cfg.schedule_cron}</code>
                      </div>
                      <div style={styles.infoRow}>
                        <span style={styles.infoLabel}>Last Run:</span>
                        <span style={styles.infoValue}>
                          {cfg.last_run ? new Date(cfg.last_run).toLocaleString('en-AU') : 'Never'}
                        </span>
                      </div>
                    </div>
                    <div style={styles.cardFooter}>
                      <button onClick={() => runBackup(cfg.id)} style={styles.secondaryBtn}>
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <polygon points="5 3 19 12 5 21 5 3"/>
                        </svg>
                        Run Now
                      </button>
                      <button onClick={() => { setEditConfig(cfg); setShowForm(true); }} style={styles.secondaryBtn}>
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                          <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                        </svg>
                        Edit
                      </button>
                      <button onClick={() => deleteConfig(cfg.id)} style={styles.dangerBtn}>
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <polyline points="3 6 5 6 21 6"/>
                          <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
                        </svg>
                        Delete
                      </button>
                     </div>
                  </div>
                ))}
              </div>
            )}
          </section>
        )}

          {activeTab === 'history' && (
            <section style={styles.section}>
              <div style={styles.sectionHeader}>
                <h2 style={styles.sectionTitle}>Backup History</h2>
                <button onClick={resetStuckJobs} style={styles.resetStuckBtn}>
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <circle cx="12" cy="12" r="10"/>
                    <line x1="12" y1="8" x2="12" y2="12"/>
                  </svg>
                  Reset Stuck Jobs
                </button>
              </div>
            ) : (
              <div style={styles.tableContainer}>
                <table style={styles.table}>
                  <thead>
                    <tr>
                      <th style={styles.th}>Job ID</th>
                      <th style={styles.th}>Status</th>
                      <th style={styles.th}>Started</th>
                      <th style={styles.th}>Completed</th>
                      <th style={styles.th}>Data Transferred</th>
                      <th style={styles.th}>Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {jobs.map(job => (
                      <tr key={job.id} style={styles.tr}>
                        <td style={styles.td}>#{job.id}</td>
                        <td style={styles.td}>
                          <span style={{...styles.statusBadge, background: getStatusColor(job.status)}}>
                            {job.status}
                          </span>
                        </td>
                        <td style={styles.td}>{new Date(job.started_at).toLocaleString('en-AU')}</td>
                        <td style={styles.td}>
                          {job.completed_at ? (
                            new Date(job.completed_at).toLocaleString('en-AU')
                          ) : job.status === 'pending' ? (
                            <span style={{color: '#6b7280'}}>
                              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" style={{verticalAlign: 'middle'}}>
                                <circle cx="12" cy="12" r="10"/>
                                <polyline points="12 6 12 12 16 14"/>
                              </svg>
                              Waiting for agent...
                            </span>
                          ) : (
                            <span style={{color: '#f59e0b'}}>⟳ Running...</span>
                          )}
                        </td>
                        <td style={styles.td}>{(job.bytes_transferred / 1024 / 1024).toFixed(2)} MB</td>
                        <td style={styles.td}>
                          <div style={{display: 'flex', gap: '8px'}}></div>
                            <button onClick={() => setSelectedJob(job.id)} style={styles.iconBtn} title="View Logs">
                              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                                <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
                                <polyline points="14 2 14 8 20 8"/>
                              </svg>
                            </button>
                          {(job.status === 'pending' || job.status === 'running') && (
                            <button onClick={() => cancelJob(job.id)} style={{...styles.iconBtn, color: '#ef4444'}} title="Cancel">
                              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                               <circle cx="12" cy="12" r="10"/>
                               <line x1="15" y1="9" x2="9" y2="15"/>
                               <line x1="9" y1="9" x2="15" y2="15"/>
                              </svg>
                            </button>
                          )}
                          </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          </section>
        )}
      </main>

      {selectedJob && (
        <LogViewer jobId={selectedJob} token={token} onClose={() => setSelectedJob(null)} />
      )}
    </div>
  );
}

const styles = {
  container: { 
    minHeight: '100vh', 
    background: 'linear-gradient(135deg, #667eea 0%, #764ba2 100%)',
    fontFamily: `-apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', sans-serif`,
    padding: '20px'
  },
  loadingContainer: { display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', minHeight: '100vh', background: '#f3f4f6' },
  spinner: { 
    width: '50px', 
    height: '50px', 
    border: '4px solid rgba(99, 102, 241, 0.1)', 
    borderTop: '4px solid #6366f1', 
    borderRadius: '50%', 
    animation: 'spin 1s linear infinite',
    marginBottom: '20px'
  },
  loadingText: { color: '#6b7280', fontSize: '16px' },
  header: { 
    background: 'rgba(255, 255, 255, 0.95)', 
    backdropFilter: 'blur(10px)',
    padding: '20px 30px', 
    borderRadius: '16px', 
    marginBottom: '24px',
    boxShadow: '0 4px 6px rgba(0, 0, 0, 0.07), 0 10px 20px rgba(0, 0, 0, 0.05)',
    display: 'flex', 
    justifyContent: 'space-between', 
    alignItems: 'center'
  },
  headerLeft: { display: 'flex', alignItems: 'center', gap: '16px' },
  logo: { 
    width: '48px', 
    height: '48px', 
    background: 'linear-gradient(135deg, #667eea 0%, #764ba2 100%)',
    borderRadius: '12px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    color: 'white'
  },
  title: { fontSize: '24px', fontWeight: '700', color: '#1f2937', margin: 0 },
  headerRight: { display: 'flex', alignItems: 'center', gap: '24px' },
  stats: { display: 'flex', gap: '32px' },
  statItem: { textAlign: 'center' },
  statValue: { display: 'block', fontSize: '24px', fontWeight: '700', color: '#667eea' },
  statLabel: { display: 'block', fontSize: '12px', color: '#6b7280', marginTop: '4px' },
  logoutBtn: { 
    background: '#ef4444', 
    color: 'white', 
    border: 'none', 
    padding: '10px 20px', 
    borderRadius: '8px', 
    cursor: 'pointer',
    fontWeight: '600',
    display: 'flex',
    alignItems: 'center',
    gap: '8px',
    transition: 'all 0.2s',
    fontSize: '14px'
  },
  tabs: { 
    background: 'rgba(255, 255, 255, 0.95)', 
    backdropFilter: 'blur(10px)',
    padding: '8px', 
    borderRadius: '12px', 
    marginBottom: '24px',
    display: 'flex',
    gap: '8px',
    boxShadow: '0 2px 4px rgba(0, 0, 0, 0.05)'
  },
  tab: { 
    background: 'transparent', 
    border: 'none', 
    padding: '12px 24px', 
    borderRadius: '8px', 
    cursor: 'pointer',
    color: '#6b7280',
    fontWeight: '600',
    fontSize: '14px',
    display: 'flex',
    alignItems: 'center',
    gap: '8px',
    transition: 'all 0.2s'
  },
  tabActive: { 
    background: 'linear-gradient(135deg, #667eea 0%, #764ba2 100%)',
    color: 'white',
    boxShadow: '0 2px 8px rgba(102, 126, 234, 0.4)'
  },
  main: { maxWidth: '1400px', margin: '0 auto' },
  section: { 
    background: 'rgba(255, 255, 255, 0.95)', 
    backdropFilter: 'blur(10px)',
    padding: '32px', 
    borderRadius: '16px',
    boxShadow: '0 4px 6px rgba(0, 0, 0, 0.07), 0 10px 20px rgba(0, 0, 0, 0.05)'
  },
  sectionHeader: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '32px' },
  sectionTitle: { fontSize: '20px', fontWeight: '700', color: '#1f2937', margin: 0 },
  primaryBtn: { 
    background: 'linear-gradient(135deg, #667eea 0%, #764ba2 100%)',
    color: 'white', 
    border: 'none', 
    padding: '12px 24px', 
    borderRadius: '8px', 
    cursor: 'pointer',
    fontWeight: '600',
    display: 'flex',
    alignItems: 'center',
    gap: '8px',
    fontSize: '14px',
    boxShadow: '0 4px 12px rgba(102, 126, 234, 0.3)',
    transition: 'all 0.2s'
  },
  secondaryBtn: { 
    background: '#f3f4f6', 
    color: '#374151', 
    border: 'none', 
    padding: '8px 16px', 
    borderRadius: '6px', 
    cursor: 'pointer',
    fontWeight: '600',
    display: 'flex',
    alignItems: 'center',
    gap: '6px',
    fontSize: '13px',
    transition: 'all 0.2s'
  },
  dangerBtn: { 
    background: '#fee2e2', 
    color: '#dc2626', 
    border: 'none', 
    padding: '8px 16px', 
    borderRadius: '6px', 
    cursor: 'pointer',
    fontWeight: '600',
    display: 'flex',
    alignItems: 'center',
    gap: '6px',
    fontSize: '13px',
    transition: 'all 0.2s'
  },
  iconBtn: {
    background: 'transparent',
    border: 'none',
    cursor: 'pointer',
    color: '#6366f1',
    padding: '8px',
    borderRadius: '6px',
    transition: 'all 0.2s',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center'
  },
  grid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(400px, 1fr))', gap: '24px' },
  card: { 
    background: 'white', 
    borderRadius: '12px', 
    overflow: 'hidden',
    boxShadow: '0 2px 4px rgba(0, 0, 0, 0.05)',
    border: '1px solid #e5e7eb',
    transition: 'all 0.2s'
  },
  cardHeader: { 
    padding: '20px', 
    borderBottom: '1px solid #e5e7eb',
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center'
  },
  cardTitle: { fontSize: '18px', fontWeight: '700', color: '#1f2937', margin: 0 },
  cardBody: { padding: '20px' },
  cardFooter: { 
    padding: '16px 20px', 
    background: '#f9fafb', 
    display: 'flex', 
    gap: '12px',
    borderTop: '1px solid #e5e7eb'
  },
  infoRow: { 
    display: 'flex', 
    justifyContent: 'space-between', 
    marginBottom: '12px',
    fontSize: '14px'
  },
  infoLabel: { color: '#6b7280', fontWeight: '500' },
  infoValue: { color: '#1f2937', fontWeight: '600', textAlign: 'right', maxWidth: '60%', overflow: 'hidden', textOverflow: 'ellipsis' },
  badge: { 
    padding: '4px 12px', 
    borderRadius: '12px', 
    fontSize: '12px', 
    fontWeight: '700',
    color: 'white',
    textTransform: 'uppercase',
    letterSpacing: '0.5px'
  },
  statusBadge: {
    display: 'inline-block',
    padding: '4px 12px',
    borderRadius: '12px',
    fontSize: '12px',
    fontWeight: '700',
    color: 'white',
    textTransform: 'capitalize'
  },
  emptyState: { 
    textAlign: 'center', 
    padding: '80px 20px',
    color: '#9ca3af'
  },
  tableContainer: { overflowX: 'auto', borderRadius: '8px', border: '1px solid #e5e7eb' },
  table: { width: '100%', borderCollapse: 'collapse', background: 'white' },
  th: { 
    textAlign: 'left', 
    padding: '16px', 
    background: '#f9fafb',
    color: '#6b7280',
    fontWeight: '600',
    fontSize: '13px',
    textTransform: 'uppercase',
    letterSpacing: '0.5px',
    borderBottom: '2px solid #e5e7eb'
  },
  tr: { transition: 'background 0.2s' },
  td: { 
    padding: '16px', 
    borderBottom: '1px solid #f3f4f6',
    fontSize: '14px',
    color: '#374151'
  },
  resetStuckBtn: { 
  background: '#f59e0b', 
  color: 'white', 
  border: 'none', 
  padding: '10px 20px', 
  borderRadius: '8px', 
  cursor: 'pointer',
  fontWeight: '600',
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  fontSize: '14px'
}
};

export default Dashboard;