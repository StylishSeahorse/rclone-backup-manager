import React, { useState } from 'react';
import axios from 'axios';

function AgentManager({ agents, onRefresh, token }) {
  const [showToken, setShowToken] = useState(false);
  const [installToken, setInstallToken] = useState('');

  const generateToken = async () => {
    try {
      const api = axios.create({ baseURL: 'http://localhost:8000/api' });
      api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
      const res = await api.post('/agents/generate-token');
      setInstallToken(res.data.token);
      setShowToken(true);
    } catch (err) {
      alert('Failed to generate token: ' + (err.response?.data?.detail || err.message));
    }
  };

  const deleteAgent = async (id) => {
    if (confirm('Remove this agent? It will need to be reinstalled.')) {
      try {
        const api = axios.create({ baseURL: 'http://localhost:8000/api' });
        api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
        await api.delete(`/agents/${id}`);
        onRefresh();
      } catch (err) {
        alert('Failed to delete agent: ' + (err.response?.data?.detail || err.message));
      }
    }
  };

  const copyToClipboard = (text) => {
    navigator.clipboard.writeText(text);
    alert('Copied to clipboard!');
  };

  const getInstallCommand = () => {
    return `curl -sSL http://YOUR-SERVER-IP:8000/api/agents/install.sh | sudo bash -s ${installToken}`;
  };

  return (
    <section style={styles.section}>
      <div style={styles.sectionHeader}>
        <div>
          <h2 style={styles.sectionTitle}>Connected Agents</h2>
          <p style={styles.subtitle}>Manage backup agents on remote devices</p>
        </div>
        <button onClick={generateToken} style={styles.primaryBtn}>
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/>
            <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
          </svg>
          Add New Agent
        </button>
      </div>

      {showToken && (
        <div style={styles.tokenCard}>
          <div style={styles.tokenCardHeader}>
            <h3 style={styles.tokenCardTitle}>Install New Agent</h3>
            <button onClick={() => setShowToken(false)} style={styles.closeBtn}>✕</button>
          </div>
          <div style={styles.tokenCardBody}>
            <p style={styles.tokenInstructions}>
              Run this command on the device you want to add as a backup agent:
            </p>
            <div style={styles.codeBlock}>
              <code style={styles.code}>{getInstallCommand()}</code>
              <button onClick={() => copyToClipboard(getInstallCommand())} style={styles.copyBtn}>
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                  <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                </svg>
                Copy
              </button>
            </div>
            <div style={styles.warning}>
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
                <line x1="12" y1="9" x2="12" y2="13"/>
                <line x1="12" y1="17" x2="12.01" y2="17"/>
              </svg>
              <div>
                <strong>Important:</strong> This token expires in 1 hour. Replace YOUR-SERVER-IP with your server's IP address.
              </div>
            </div>
          </div>
        </div>
      )}

      {agents.length === 0 ? (
        <div style={styles.emptyState}>
          <svg width="64" height="64" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5">
            <rect x="2" y="3" width="20" height="14" rx="2" ry="2"/>
            <line x1="8" y1="21" x2="16" y2="21"/>
            <line x1="12" y1="17" x2="12" y2="21"/>
          </svg>
          <h3 style={styles.emptyTitle}>No agents connected</h3>
          <p style={styles.emptyText}>Install an agent on a device to start backing up files</p>
          <button onClick={generateToken} style={styles.primaryBtn}>
            Add Your First Agent
          </button>
        </div>
      ) : (
        <div style={styles.grid}>
          {agents.map(agent => (
            <div key={agent.id} style={styles.card}>
              <div style={styles.cardHeader}>
                <div style={styles.agentInfo}>
                  <h3 style={styles.cardTitle}>{agent.hostname}</h3>
                  <span style={{...styles.statusDot, background: agent.status === 'online' ? '#10b981' : '#ef4444'}}/>
                </div>
                <span style={{...styles.badge, background: agent.status === 'online' ? '#10b981' : '#6b7280'}}>
                  {agent.status}
                </span>
              </div>
              <div style={styles.cardBody}>
                <div style={styles.infoRow}>
                  <span style={styles.infoLabel}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" style={{verticalAlign: 'middle', marginRight: '4px'}}>
                      <rect x="2" y="3" width="20" height="14" rx="2" ry="2"/>
                    </svg>
                    Platform:
                  </span>
                  <span style={styles.infoValue}>{agent.platform || 'Unknown'}</span>
                </div>
                <div style={styles.infoRow}>
                  <span style={styles.infoLabel}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" style={{verticalAlign: 'middle', marginRight: '4px'}}>
                      <circle cx="12" cy="12" r="10"/>
                      <line x1="2" y1="12" x2="22" y2="12"/>
                      <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
                    </svg>
                    IP Address:
                  </span>
                  <code style={styles.infoValue}>{agent.ip_address}</code>
                </div>
                <div style={styles.infoRow}>
                  <span style={styles.infoLabel}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" style={{verticalAlign: 'middle', marginRight: '4px'}}>
                      <circle cx="12" cy="12" r="10"/>
                      <polyline points="12 6 12 12 16 14"/>
                    </svg>
                    Last Seen:
                  </span>
                  <span style={styles.infoValue}>
                    {agent.last_seen ? new Date(agent.last_seen).toLocaleString('en-AU') : 'Never'}
                  </span>
                </div>
                <div style={styles.infoRow}>
                  <span style={styles.infoLabel}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" style={{verticalAlign: 'middle', marginRight: '4px'}}>
                      <path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>
                      <polyline points="9 22 9 12 15 12 15 22"/>
                    </svg>
                    Version:
                  </span>
                  <span style={styles.infoValue}>{agent.version || 'v1.0.0'}</span>
                </div>
              </div>
              <div style={styles.cardFooter}>
                <button 
                  onClick={() => deleteAgent(agent.id)} 
                  style={{
                    ...styles.dangerBtn,
                    opacity: agent.status === 'online' ? 0.5 : 1,
                    cursor: agent.status === 'online' ? 'not-allowed' : 'pointer'
                  }}
                  disabled={agent.status === 'online'}
                >
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <polyline points="3 6 5 6 21 6"/>
                    <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
                  </svg>
                  Remove
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

const styles = {
  section: { 
    background: 'rgba(255, 255, 255, 0.95)', 
    backdropFilter: 'blur(10px)',
    padding: '32px', 
    borderRadius: '16px',
    boxShadow: '0 4px 6px rgba(0, 0, 0, 0.07), 0 10px 20px rgba(0, 0, 0, 0.05)'
  },
  sectionHeader: { display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '32px' },
  sectionTitle: { fontSize: '24px', fontWeight: '700', color: '#1f2937', margin: '0 0 8px 0' },
  subtitle: { fontSize: '14px', color: '#6b7280', margin: 0 },
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
  tokenCard: {
    background: '#eff6ff',
    border: '2px solid #3b82f6',
    borderRadius: '12px',
    marginBottom: '32px',
    overflow: 'hidden'
  },
  tokenCardHeader: {
    padding: '20px',
    background: '#3b82f6',
    color: 'white',
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center'
  },
  tokenCardTitle: { fontSize: '18px', fontWeight: '700', margin: 0 },
  closeBtn: {
    background: 'rgba(255, 255, 255, 0.2)',
    border: 'none',
    color: 'white',
    width: '32px',
    height: '32px',
    borderRadius: '50%',
    cursor: 'pointer',
    fontSize: '18px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    transition: 'all 0.2s'
  },
  tokenCardBody: { padding: '24px' },
  tokenInstructions: { color: '#1e40af', marginBottom: '16px', fontSize: '14px', fontWeight: '500' },
  codeBlock: {
    background: '#1f2937',
    borderRadius: '8px',
    padding: '16px',
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    gap: '16px',
    marginBottom: '16px'
  },
  code: {
    color: '#10b981',
    fontFamily: 'Consolas, Monaco, monospace',
    fontSize: '13px',
    flex: 1,
    wordBreak: 'break-all'
  },
  copyBtn: {
    background: '#374151',
    color: 'white',
    border: 'none',
    padding: '8px 12px',
    borderRadius: '6px',
    cursor: 'pointer',
    display: 'flex',
    alignItems: 'center',
    gap: '6px',
    fontSize: '13px',
    fontWeight: '600',
    whiteSpace: 'nowrap'
  },
  warning: {
    background: '#fef3c7',
    border: '1px solid #fbbf24',
    borderRadius: '8px',
    padding: '12px',
    display: 'flex',
    gap: '12px',
    color: '#92400e',
    fontSize: '13px',
    alignItems: 'flex-start'
  },
  emptyState: { 
    textAlign: 'center', 
    padding: '80px 20px',
    color: '#9ca3af'
  },
  emptyTitle: { fontSize: '20px', fontWeight: '600', color: '#4b5563', margin: '16px 0 8px' },
  emptyText: { fontSize: '14px', color: '#9ca3af', marginBottom: '24px' },
  grid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(350px, 1fr))', gap: '24px' },
  card: { 
    background: 'white', 
    borderRadius: '12px', 
    overflow: 'hidden',
    boxShadow: '0 2px 4px rgba(0, 0, 0, 0.05)',
    border: '1px solid #e5e7eb',
    transition: 'all 0.3s'
  },
  cardHeader: { 
    padding: '20px', 
    borderBottom: '1px solid #e5e7eb',
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center'
  },
  agentInfo: { display: 'flex', alignItems: 'center', gap: '12px' },
  cardTitle: { fontSize: '18px', fontWeight: '700', color: '#1f2937', margin: 0 },
  statusDot: {
    width: '10px',
    height: '10px',
    borderRadius: '50%',
    animation: 'pulse 2s infinite',
    boxShadow: '0 0 8px currentColor'
  },
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
    fontSize: '14px',
    alignItems: 'center'
  },
  infoLabel: { color: '#6b7280', fontWeight: '500', display: 'flex', alignItems: 'center' },
  infoValue: { color: '#1f2937', fontWeight: '600' },
  badge: { 
    padding: '4px 12px', 
    borderRadius: '12px', 
    fontSize: '12px', 
    fontWeight: '700',
    color: 'white',
    textTransform: 'uppercase',
    letterSpacing: '0.5px'
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
    transition: 'all 0.2s',
    flex: 1
  }
};

export default AgentManager;