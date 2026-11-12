import React, { useState, useEffect, useCallback } from "react";

const API_BASE = import.meta.env.VITE_API_BASE || "http://118.31.111.223:8080";

interface Plan {
  id: number;
  name: string;
  token_amount: number;
  price_cents: number;
  discount_rate: number;
}

interface APIKey {
  id: number;
  key: string;
  name: string;
  is_active: boolean;
  created_at: string;
}


function App() {
  const [token, setToken] = useState<string>(localStorage.getItem("token") || "");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [balance, setBalance] = useState<number>(0);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [keyName, setKeyName] = useState("");
  const [newKey, setNewKey] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  const [error, setError] = useState("");
  const [page, setPage] = useState<"login" | "dashboard">("login");

  useEffect(() => {
    if (token) setPage("dashboard");
  }, [token]);

  const auth = useCallback(async (endpoint: string) => {
    setError("");
    try {
      const res = await fetch(`${API_BASE}/api/${endpoint}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
      });
      const data = await res.json();
      if (!res.ok) {
        setError(data.error || "Something went wrong");
        return;
      }
      if (data.token) {
        setToken(data.token);
        localStorage.setItem("token", data.token);
      }
    } catch {
      setError("Network error");
    }
  }, [username, password]);

  const fetchData = useCallback(async () => {
    if (!token) return;
    const headers = { Authorization: `Bearer ${token}` };
    try {
      const [balRes, planRes, keyRes] = await Promise.all([
        fetch(`${API_BASE}/api/balance`, { headers }),
        fetch(`${API_BASE}/api/plans`),
        fetch(`${API_BASE}/api/keys`, { headers }),
      ]);
      if (balRes.ok) setBalance((await balRes.json()).balance);
      if (planRes.ok) setPlans(await planRes.json());
      if (keyRes.ok) setKeys(await keyRes.json());
    } catch {
      setError("Failed to load data");
    }
  }, [token]);

  const createKey = useCallback(async () => {
    setError("");
    setMessage("");
    setNewKey("");
    setBusy(true);
    try {
      const res = await fetch(`${API_BASE}/api/keys`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify({ name: keyName }),
      });
      const data = await res.json();
      if (!res.ok) {
        setError(data.error || "Failed to create key");
        return;
      }
      setNewKey(data.key);
      setKeyName("");
      await fetchData();
    } catch {
      setError("Network error");
    } finally {
      setBusy(false);
    }
  }, [token, keyName, fetchData]);

  const buyPlan = useCallback(async (planID: number) => {
    setError("");
    setMessage("");
    setBusy(true);
    try {
      const headers = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
      const createRes = await fetch(`${API_BASE}/api/orders`, {
        method: "POST",
        headers,
        body: JSON.stringify({ plan_id: planID }),
      });
      const order = await createRes.json();
      if (!createRes.ok) {
        setError(order.error || "Failed to create order");
        return;
      }
      const payRes = await fetch(`${API_BASE}/api/orders/${order.id}/pay`, { method: "POST", headers });
      const paid = await payRes.json();
      if (!payRes.ok) {
        setError(paid.error || "Failed to pay order");
        return;
      }
      setMessage(`Order #${order.id} paid: +${paid.token_amount.toLocaleString()} tokens`);
      await fetchData();
    } catch {
      setError("Network error");
    } finally {
      setBusy(false);
    }
  }, [token, fetchData]);

  useEffect(() => {
    if (token) fetchData();
  }, [token, fetchData]);

  const logout = () => {
    setToken("");
    localStorage.removeItem("token");
    setPage("login");
  };

  if (page === "login") {
    return (
      <div style={styles.container}>
        <div style={styles.card}>
          <h1 style={styles.title}>LLM API Gateway</h1>
          <input style={styles.input} placeholder="Username" value={username} onChange={e => setUsername(e.target.value)} />
          <input style={styles.input} type="password" placeholder="Password" value={password} onChange={e => setPassword(e.target.value)} />
          <div style={{ display: "flex", gap: 8 }}>
            <button style={styles.button} onClick={() => auth("login")}>Login</button>
            <button style={{ ...styles.button, background: "#2563eb" }} onClick={() => auth("register")}>Register</button>
          </div>
          {error && <p style={styles.error}>{error}</p>}
        </div>
      </div>
    );
  }

  return (
    <div style={styles.container}>
      <nav style={styles.nav}>
        <span style={styles.navTitle}>LLM API Gateway</span>
        <span style={{ marginLeft: "auto", marginRight: 16 }}>Balance: {balance.toLocaleString()} tokens</span>
        <button style={styles.navButton} onClick={logout}>Logout</button>
      </nav>
      <div style={styles.content}>
        {error && <p style={styles.error}>{error}</p>}
        {message && <p style={styles.success}>{message}</p>}

        <section style={styles.section}>
          <h2>API Keys</h2>
          <div style={{ display: "flex", gap: 8, marginBottom: 12 }}>
            <input
              style={{ ...styles.input, marginBottom: 0 }}
              placeholder="Key name"
              value={keyName}
              onChange={e => setKeyName(e.target.value)}
            />
            <button style={styles.button} onClick={createKey} disabled={busy}>Create key</button>
          </div>
          {newKey && (
            <p style={styles.newKey}>New key: <code>{newKey}</code></p>
          )}
          <table style={styles.table}>
            <thead>
              <tr><th>Name</th><th>Key</th><th>Status</th><th>Created</th></tr>
            </thead>
            <tbody>
              {keys.map(k => (
                <tr key={k.id}>
                  <td>{k.name || "-"}</td>
                  <td style={{ fontFamily: "monospace", fontSize: 12 }}>{k.key.slice(0, 16)}...</td>
                  <td>{k.is_active ? "Active" : "Disabled"}</td>
                  <td>{new Date(k.created_at).toLocaleDateString()}</td>
                </tr>
              ))}
              {keys.length === 0 && <tr><td colSpan={4}>No API keys yet</td></tr>}
            </tbody>
          </table>
        </section>

        <section style={styles.section}>
          <h2>Plans</h2>
          <div style={styles.planGrid}>
            {plans.map(p => (
              <div key={p.id} style={styles.planCard}>
                <h3>{p.name}</h3>
                <p style={styles.planAmount}>{p.token_amount.toLocaleString()} tokens</p>
                <p>¥{(p.price_cents / 100).toFixed(2)}</p>
                {p.discount_rate < 1 && <p style={{ color: "#16a34a" }}>Save {Math.round((1 - p.discount_rate) * 100)}%</p>}
                <button style={styles.button} onClick={() => buyPlan(p.id)} disabled={busy}>Buy</button>
              </div>
            ))}
          </div>
          <p style={styles.note}>Payment is a demo endpoint and is disabled unless ALLOW_MOCK_PAYMENT=true.</p>
        </section>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { minHeight: "100vh", background: "#f8fafc", fontFamily: "system-ui, sans-serif" },
  card: { maxWidth: 400, margin: "100px auto", padding: 32, background: "#fff", borderRadius: 12, boxShadow: "0 4px 6px rgba(0,0,0,0.1)" },
  title: { textAlign: "center", marginBottom: 24, color: "#1e293b" },
  input: { width: "100%", padding: "10px 12px", marginBottom: 12, border: "1px solid #d1d5db", borderRadius: 6, fontSize: 14, boxSizing: "border-box" },
  button: { flex: 1, padding: "10px 20px", background: "#1e40af", color: "#fff", border: "none", borderRadius: 6, cursor: "pointer", fontSize: 14 },
  navButton: { padding: "6px 16px", background: "#ef4444", color: "#fff", border: "none", borderRadius: 6, cursor: "pointer" },
  error: { color: "#dc2626", marginTop: 12, fontSize: 14 },
  success: { color: "#15803d", marginTop: 12, fontSize: 14 },
  newKey: { background: "#ecfdf5", border: "1px solid #86efac", padding: 12, borderRadius: 6, wordBreak: "break-all" },
  note: { color: "#64748b", fontSize: 13, marginTop: 12 },
  nav: { display: "flex", alignItems: "center", padding: "12px 24px", background: "#1e293b", color: "#fff" },
  navTitle: { fontWeight: 600, fontSize: 18 },
  content: { maxWidth: 960, margin: "0 auto", padding: 24 },
  section: { marginBottom: 32 },
  table: { width: "100%", borderCollapse: "collapse", background: "#fff", borderRadius: 8, overflow: "hidden", boxShadow: "0 1px 3px rgba(0,0,0,0.1)" },
  planGrid: { display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(200px, 1fr))", gap: 16 },
  planCard: { background: "#fff", padding: 24, borderRadius: 8, boxShadow: "0 1px 3px rgba(0,0,0,0.1)", textAlign: "center" },
  planAmount: { fontSize: 24, fontWeight: 700, color: "#1e40af" },
};

export default App;


