import React, { useState, useEffect } from 'react';
import axios from 'axios';

function ProfileManager({ token, onClose, onSelect }) {
  const [profiles, setProfiles] = useState([]);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState({
    name: '',
    remote_type: 's3',
    credentials: { access_key: '', secret_key: '' },
    region: 'us-east-1',
    endpoint: 's3.wasabisys.com'
  });

  useEffect(() => {
    loadProfiles();
  }, []);

  const loadProfiles = async () => {
    const api = axios.create({ baseURL: '/api' });
    api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
    const res = await api.get('/profiles');
    setProfiles(res.data);
  };

  const saveProfile = async (e) => {
    e.preventDefault();
    try {
      const api = axios.create({ baseURL: '/api' });
      api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
      await api.post('/profiles', form);
      setShowForm(false);
      loadProfiles();
      setForm({ name: '', remote_type: 's3', credentials: {}, region: 'us-east-1', endpoint: 's3.wasabisys.com' });
    } catch (err) {
      alert('Failed to save: ' + (err.response?.data?.detail || err.message));
    }
  };

  const deleteProfile = async (id) => {
    if (confirm('Delete this profile?')) {
      const api = axios.create({ baseURL: '/api' });
      api.defaults.headers.common['Authorization'] = `Bearer ${token}`;
      await api.delete(`/profiles/${id}`);
      loadProfiles();
    }
  };

  return (
    <div style={styles.overlay}>
      <div style={styles.modal}>
        <div style={styles.modalHeader}>
          <h3 style={styles.modalTitle}>Remote Credential Profiles</h3>
          <button onClick={onClose} style={styles.closeBtn}>✕</button>
        </div>

        <div style={styles.modalBody}>
          <div style={styles.actions}>
            <button onClick={() => setShowForm(!showForm)} style={styles.primaryBtn}>
              {showForm ? 'Cancel' : '+ New Profile'}
            </button>
          </div>

          {showForm && (
            <form onSubmit={saveProfile} style={styles.form}>
              <div style={styles.formGrid}>
                <div style={styles.formGroup}>
                  <label style={styles.label}>Profile Name *</label>
                  <input
                    style={styles.input}
                    value={form.name}
                    onChange={e => setForm({...form, name: e.target.value})}
                    placeholder="e.g., Production Wasabi"
                    required
                  />
                </div>

                <div style={styles.formGroup}>
                  <label style={styles.label}>Type *</label>
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
                        required
                      />
                    </div>
                    <div style={styles.formGroup}>
                      <label style={styles.label}>Secret Key *</label>
                      <input
                        style={styles.input}
                        type="password"
                        value={form.credentials.secret_key || ''}
                        onChange={e => setForm({...form, credentials: {...form.credentials, secret_key: e.target.value}})}
                        required
                      />
                    </div>
                    <div style={styles.formGroup}>
                      <label style={styles.label}>Region</label>
                      <input
                        style={styles.input}
                        value={form.region}
                        onChange={e => setForm({...form, region: e.target.value})}
                      />
                    </div>
                    <div style={styles.formGroup}>
                      <label style={styles.label}>Endpoint</label>
                      <input
                        style={styles.input}
                        value={form.endpoint}
                        onChange={e => setForm({...form, endpoint: e.target.value})}
                      />
                    </div>
                  </>
                )}
              </div>
              <div style={styles.formFooter}>
                <button type="submit" style={styles.submitBtn}>Save Profile</button>
              </div>
            </form>
          )}

          <div style={styles.profileList}>
            {profiles.map(profile => (
              <div key={profile.id} style={styles.profileCard}>
                <div style={styles.profileHeader}>
                  <h4 style={styles.profileName}>{profile.name}</h4>
                  <span style={styles.profileType}>{profile.remote_type.toUpperCase()}</span>
                </div>
                <div style={styles.profileDetails}>
                  {profile.region && <span>Region: {profile.region}</span>}
                  {profile.endpoint && <span>Endpoint: {profile.endpoint}</span>}
                </div>
                <div style={styles.profileActions}>
                  {onSelect && (
                    <button onClick={() => { onSelect(profile); onClose(); }} style={styles.selectBtn}>
                      Use This Profile
                    </button>
                  )}
                  <button onClick={() => deleteProfile(profile.id)} style={styles.deleteBtn}>
                    Delete
                  </button>
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

const styles = {
  overlay: { position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0, 0, 0, 0.5)', backdropFilter: 'blur(4px)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 2000, padding: '20px' },
  modal: { background: 'white', borderRadius: '16px', maxWidth: '800px', width: '100%', maxHeight: '90vh', overflow: 'auto', boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.1)' },
  modalHeader: { padding: '24px', borderBottom: '1px solid #e5e7eb', display: 'flex', justifyContent: 'space-between', alignItems: 'center', position: 'sticky', top: 0, background: 'white', zIndex: 1 },
  modalTitle: { fontSize: '20px', fontWeight: '700', color: '#1f2937', margin: 0 },
  closeBtn: { background: 'transparent', border: 'none', fontSize: '24px', color: '#6b7280', cursor: 'pointer', width: '32px', height: '32px', borderRadius: '6px' },
  modalBody: { padding: '24px' },
  actions: { marginBottom: '24px' },
  primaryBtn: { background: 'linear-gradient(135deg, #667eea 0%, #764ba2 100%)', color: 'white', border: 'none', padding: '10px 20px', borderRadius: '8px', cursor: 'pointer', fontWeight: '600', fontSize: '14px' },
  form: { background: '#f9fafb', padding: '20px', borderRadius: '12px', marginBottom: '24px' },
  formGrid: { display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '16px' },
  formGroup: { display: 'flex', flexDirection: 'column' },
  label: { fontSize: '14px', fontWeight: '600', color: '#374151', marginBottom: '8px' },
  input: { padding: '10px 14px', border: '1px solid #d1d5db', borderRadius: '8px', fontSize: '14px' },
  select: { padding: '10px 14px', border: '1px solid #d1d5db', borderRadius: '8px', fontSize: '14px', background: 'white' },
  formFooter: { marginTop: '16px', display: 'flex', justifyContent: 'flex-end' },
  submitBtn: { background: '#10b981', color: 'white', border: 'none', padding: '10px 20px', borderRadius: '8px', cursor: 'pointer', fontWeight: '600' },
  profileList: { display: 'grid', gap: '16px' },
  profileCard: { background: 'white', border: '1px solid #e5e7eb', borderRadius: '12px', padding: '20px' },
  profileHeader: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' },
  profileName: { fontSize: '16px', fontWeight: '700', color: '#1f2937', margin: 0 },
  profileType: { fontSize: '12px', fontWeight: '700', color: '#6366f1', background: '#e0e7ff', padding: '4px 10px', borderRadius: '6px' },
  profileDetails: { fontSize: '13px', color: '#6b7280', marginBottom: '16px', display: 'flex', flexDirection: 'column', gap: '4px' },
  profileActions: { display: 'flex', gap: '8px' },
  selectBtn: { background: '#3b82f6', color: 'white', border: 'none', padding: '8px 16px', borderRadius: '6px', cursor: 'pointer', fontWeight: '600', fontSize: '13px', flex: 1 },
  deleteBtn: { background: '#fee2e2', color: '#dc2626', border: 'none', padding: '8px 16px', borderRadius: '6px', cursor: 'pointer', fontWeight: '600', fontSize: '13px' }
};

export default ProfileManager;