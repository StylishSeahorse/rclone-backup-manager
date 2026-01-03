import React, { useState, useEffect } from 'react';
import axios from 'axios';
import ProfileManager from './ProfileManager';

function ConfigForm({ token, agents = [], onSave, onCancel, editConfig = null }) {
  const [profiles, setProfiles] = useState([]);
  const [showProfileManager, setShowProfileManager] = useState(false);
  
  // Determine if editing used a profile or manual creds
  const [useProfile, setUseProfile] = useState(
    editConfig ? (editConfig.remote_profile_id != null) : false
  );
  
  const [form, setForm] = useState(editConfig ? {
    ...editConfig,
    // Preserve existing credentials (they're encrypted in editConfig)
    // Don't reset them - user can change if needed
    credentials: editConfig.credentials || {
      access_key: '',
      secret_key: '',
      region: 'us-east-1',
      endpoint: 's3.wasabisys.com'
    }
  } : {
    name: '',
    agent_id: null,
    source_path: '',
    remote_profile_id: null,
    remote_type: 's3',
    remote_name: '',
    remote_path: '',
    credentials: { access_key: '', secret_key: '', region: 'us-east-1', endpoint: 's3.wasabisys.com' },
    is_incremental: true,
    schedule_cron: '0 2 * * *',
    keep_daily_days: 3,
    keep_weekly: true,
    enabled: true
  });

  useEffect(() => {
    loadProfiles();
  }, []);

  const loadProfiles = async () => {
    const api = axios.create({ baseURL: 'http://localhost:8000/api' });
    api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
    const res = await api.get('/profiles');
    setProfiles(res.data);
  };

  const handleProfileSelect = (profile) => {
    setForm({
      ...form,
      remote_profile_id: profile.id,
      remote_type: profile.remote_type,
      credentials: {}  // Clear manual creds
    });
    setUseProfile(true);
  };

  const handleSubmit = async (e) => {
    e.preventDefault();
    const api = axios.create({ baseURL: 'http://localhost:8000/api' });
    api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
    
    try {
      const payload = {...form};
      if (useProfile && !payload.remote_profile_id) {
        alert('Please select a saved profile or enter credentials manually');
        return;
      }
      
      if (editConfig) {
        await api.put(`/configs/${editConfig.id}`, payload);
      } else {
        await api.post('/configs', payload);
      }
      onSave();
    } catch (err) {
      alert('Failed to save: ' + (err.response?.data?.detail || err.message));
    }
  };

  const selectedProfile = profiles.find(p => p.id === form.remote_profile_id);

  return (
    <div style={styles.overlay}>
      <div style={styles.modal}>
        <div style={styles.modalHeader}>
          <h3 style={styles.modalTitle}>{editConfig ? 'Edit' : 'New'} Backup Job</h3>
          <button onClick={onCancel} style={styles.closeBtn}>✕</button>
        </div>
        
        <form onSubmit={handleSubmit} style={styles.form}>
          <div style={styles.formGrid}>
            <div style={styles.formGroup}>
              <label style={styles.label}>Job Name *</label>
              <input 
                style={styles.input}
                value={form.name} 
                onChange={e => setForm({...form, name: e.target.value})} 
                required 
                placeholder="e.g., Documents Backup"
              />
            </div>

            {agents.length > 0 && (
              <div style={styles.formGroup}>
                <label style={styles.label}>Agent</label>
                <select 
                  style={styles.select}
                  value={form.agent_id || ''} 
                  onChange={e => setForm({...form, agent_id: e.target.value ? parseInt(e.target.value) : null})}
                >
                  <option value="">Local (This Server)</option>
                  {agents.map(agent => (
                    <option key={agent.id} value={agent.id}>
                      {agent.hostname} ({agent.ip_address})
                    </option>
                  ))}
                </select>
              </div>
            )}

            <div style={styles.formGroup}>
              <label style={styles.label}>Source Path *</label>
              <input 
                style={styles.input}
                value={form.source_path} 
                onChange={e => setForm({...form, source_path: e.target.value})} 
                required 
                placeholder="/path/to/backup"
              />
            </div>

            <div style={styles.formGroup}>
              <label style={styles.label}>Remote Name *</label>
              <input 
                style={styles.input}
                value={form.remote_name} 
                onChange={e => setForm({...form, remote_name: e.target.value})} 
                required 
                placeholder="wasabi"
              />
            </div>

            <div style={styles.formGroup}>
              <label style={styles.label}>Remote Path (bucket/path) *</label>
              <input 
                style={styles.input}
                value={form.remote_path} 
                onChange={e => setForm({...form, remote_path: e.target.value})} 
                required 
                placeholder="my-bucket/backups"
              />
            </div>
          </div>

          <div style={styles.credSection}>
            <div style={styles.credHeader}>
              <h4 style={styles.credTitle}>Credentials</h4>
              <div style={styles.credToggle}>
                <button
                  type="button"
                  onClick={() => setUseProfile(true)}
                  style={{...styles.toggleBtn, ...(useProfile ? styles.toggleBtnActive : {})}}
                >
                  Use Saved Profile
                </button>
                <button
                  type="button"
                  onClick={() => { setUseProfile(false); setForm({...form, remote_profile_id: null}); }}
                  style={{...styles.toggleBtn, ...(!useProfile ? styles.toggleBtnActive : {})}}
                >
                  Enter Manually
                </button>
              </div>
            </div>

            {useProfile ? (
              <div style={styles.profileSelector}>
                {selectedProfile ? (
                  <div style={styles.selectedProfile}>
                    <div>
                      <strong>{selectedProfile.name}</strong>
                      <div style={{fontSize: '12px', color: '#6b7280', marginTop: '4px'}}>
                        {selectedProfile.remote_type.toUpperCase()} • {selectedProfile.region}
                      </div>
                    </div>
                    <button
                      type="button"
                      onClick={() => setShowProfileManager(true)}
                      style={styles.changeBtn}
                    >
                      Change
                    </button>
                  </div>
                ) : (
                  <button
                    type="button"
                    onClick={() => setShowProfileManager(true)}
                    style={styles.selectProfileBtn}
                  >
                    Select Saved Profile
                  </button>
                )}
              </div>
            ) : (
              <div style={styles.formGrid}>
                <div style={styles.formGroup}>
                  <label style={styles.label}>Remote Type *</label>
                  <select 
                    style={styles.select}
                    value={form.remote_type} 
                    onChange={e => setForm({...form, remote_type: e.target.value})}
                  >
                    <option value="s3">S3 (Wasabi/AWS)</option>
                    <option value="gdrive">Google Drive</option>
                  </select>
                </div>

                {form.remote_type === 's3' && (
                  <>
                    <div style={styles.formGroup}>
                      <label style={styles.label}>Access Key *</label>
                      <input 
                        style={styles.input}
                        value={form.credentials.access_key || ''} 
                        onChange={e => setForm({...form, credentials: {...form.credentials, access_key: e.target.value}})} 
                        required={!useProfile && !editConfig}  // ← Not required when editing
                        placeholder={editConfig ? "Leave blank to keep existing" : "Enter access key"}
                      />
                    </div>
                    <div style={styles.formGroup}>
                      <label style={styles.label}>Secret Key *</label>
                      <input 
                        style={styles.input}
                        type="password" 
                        value={form.credentials.secret_key || ''} 
                        onChange={e => setForm({...form, credentials: {...form.credentials, secret_key: e.target.value}})} 
                        required={!useProfile && !editConfig}  // ← Not required when editing
                        placeholder={editConfig ? "Leave blank to keep existing" : "Enter secret key"}
                      />
                    </div>
                    <div style={styles.formGroup}>
                      <label style={styles.label}>Region</label>
                      <input 
                        style={styles.input}
                        value={form.credentials.region || 'us-east-1'} 
                        onChange={e => setForm({...form, credentials: {...form.credentials, region: e.target.value}})}
                      />
                    </div>
                    <div style={styles.formGroup}>
                      <label style={styles.label}>Endpoint</label>
                      <input 
                        style={styles.input}
                        value={form.credentials.endpoint || 's3.wasabisys.com'} 
                        onChange={e => setForm({...form, credentials: {...form.credentials, endpoint: e.target.value}})}
                      />
                    </div>
                  </>
                )}
              </div>
            )}
          </div>

          <div style={styles.formGrid}>
            <div style={styles.formGroup}>
              <label style={styles.label}>Schedule (Cron) *</label>
              <input 
                style={styles.input}
                value={form.schedule_cron} 
                onChange={e => setForm({...form, schedule_cron: e.target.value})} 
                required
              />
              <small style={styles.hint}>Default: Daily at 2:00 AM</small>
            </div>
            <div style={styles.formGroup}>
              <label style={styles.label}>Keep Daily (days)</label>
              <input 
                style={styles.input}
                type="number" 
                value={form.keep_daily_days} 
                onChange={e => setForm({...form, keep_daily_days: parseInt(e.target.value)})}
                min="1"
              />
            </div>
          </div>

          <div style={styles.checkboxGroup}>
            <label style={styles.checkboxLabel}>
              <input 
                type="checkbox" 
                checked={form.is_incremental} 
                onChange={e => setForm({...form, is_incremental: e.target.checked})}
                style={styles.checkbox}
              />
              Incremental Backup
            </label>
            <label style={styles.checkboxLabel}>
              <input 
                type="checkbox" 
                checked={form.keep_weekly} 
                onChange={e => setForm({...form, keep_weekly: e.target.checked})}
                style={styles.checkbox}
              />
              Keep Weekly Backup
            </label>
            <label style={styles.checkboxLabel}>
              <input 
                type="checkbox" 
                checked={form.enabled} 
                onChange={e => setForm({...form, enabled: e.target.checked})}
                style={styles.checkbox}
              />
              Enabled
            </label>
          </div>

          <div style={styles.modalFooter}>
            <button type="button" onClick={onCancel} style={styles.cancelBtn}>Cancel</button>
            <button type="submit" style={styles.submitBtn2}>
              {editConfig ? 'Update Job' : 'Create Backup Job'}
            </button>
          </div>
        </form>
      </div>

      {showProfileManager && (
        <ProfileManager 
          token={token} 
          onClose={() => setShowProfileManager(false)}
          onSelect={handleProfileSelect}
        />
      )}
    </div>
  );
}

const styles = {
  overlay: { position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0, 0, 0, 0.5)', backdropFilter: 'blur(4px)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000, padding: '20px' },
  modal: { background: 'white', borderRadius: '16px', maxWidth: '900px', width: '100%', maxHeight: '90vh', overflow: 'auto', boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.1)' },
  modalHeader: { padding: '24px', borderBottom: '1px solid #e5e7eb', display: 'flex', justifyContent: 'space-between', alignItems: 'center', position: 'sticky', top: 0, background: 'white', zIndex: 1 },
  modalTitle: { fontSize: '20px', fontWeight: '700', color: '#1f2937', margin: 0 },
  closeBtn: { background: 'transparent', border: 'none', fontSize: '24px', color: '#6b7280', cursor: 'pointer', width: '32px', height: '32px', borderRadius: '6px' },
  form: { padding: '24px' },
  formGrid: { display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '20px', marginBottom: '24px' },
  formGroup: { display: 'flex', flexDirection: 'column' },
  label: { fontSize: '14px', fontWeight: '600', color: '#374151', marginBottom: '8px' },
  input: { padding: '10px 14px', border: '1px solid #d1d5db', borderRadius: '8px', fontSize: '14px', fontFamily: 'inherit' },
  select: { padding: '10px 14px', border: '1px solid #d1d5db', borderRadius: '8px', fontSize: '14px', background: 'white', fontFamily: 'inherit' },
  hint: { fontSize: '12px', color: '#6b7280', marginTop: '4px' },
  credSection: { background: '#f9fafb', padding: '20px', borderRadius: '12px', marginBottom: '24px' },
  credHeader: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' },
  credTitle: { fontSize: '16px', fontWeight: '700', color: '#1f2937', margin: 0 },
  credToggle: { display: 'flex', gap: '8px', background: 'white', padding: '4px', borderRadius: '8px' },
  toggleBtn: { padding: '6px 16px', border: 'none', background: 'transparent', color: '#6b7280', borderRadius: '6px', cursor: 'pointer', fontSize: '13px', fontWeight: '600' },
  toggleBtnActive: { background: 'linear-gradient(135deg, #667eea 0%, #764ba2 100%)', color: 'white', boxShadow: '0 2px 4px rgba(102, 126, 234, 0.3)' },
  profileSelector: { marginTop: '12px' },
  selectedProfile: { background: 'white', border: '2px solid #3b82f6', borderRadius: '8px', padding: '16px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' },
  selectProfileBtn: { width: '100%', padding: '12px', background: 'white', border: '2px dashed #d1d5db', borderRadius: '8px', cursor: 'pointer', color: '#6b7280', fontWeight: '600' },
  changeBtn: { background: '#3b82f6', color: 'white', border: 'none', padding: '8px 16px', borderRadius: '6px', cursor: 'pointer', fontWeight: '600', fontSize: '13px' },
  checkboxGroup: { display: 'flex', gap: '24px', marginBottom: '24px', padding: '16px', background: '#f9fafb', borderRadius: '8px' },
  checkboxLabel: { display: 'flex', alignItems: 'center', gap: '8px', fontSize: '14px', fontWeight: '500', color: '#374151', cursor: 'pointer' },
  checkbox: { width: '18px', height: '18px', cursor: 'pointer' },
  modalFooter: { display: 'flex', justifyContent: 'flex-end', gap: '12px', paddingTop: '24px', borderTop: '1px solid #e5e7eb' },
  cancelBtn: { padding: '10px 20px', border: '1px solid #d1d5db', background: 'white', color: '#374151', borderRadius: '8px', cursor: 'pointer', fontWeight: '600', fontSize: '14px' },
  submitBtn2: { padding: '10px 20px', border: 'none', background: 'linear-gradient(135deg, #667eea 0%, #764ba2 100%)', color: 'white', borderRadius: '8px', cursor: 'pointer', fontWeight: '600', fontSize: '14px', boxShadow: '0 4px 12px rgba(102, 126, 234, 0.3)' }
};

export default ConfigForm;