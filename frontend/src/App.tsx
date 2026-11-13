import React, { useState, useEffect, useCallback } from "react";

const API_BASE = import.meta.env.VITE_API_BASE ?? "http://118.31.111.223:8080";

interface Plan {
  id: number;
  name: string;
  token_amount: number;
  price_cents: number;
  discount_rate: number;
}

interface APIKey {
  id: number;
  key_prefix: string;
  name: string;
  is_active: boolean;
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
}

interface Order {
  id: number;
  plan_id: number;
  amount_cents: number;
  status: string;
  created_at: string;
  paid_at: string | null;
}


function App() {
  const [token, setToken] = useState<string>(localStorage.getItem("token") || "");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [inviteCode, setInviteCode] = useState("");
  const [balance, setBalance] = useState<number>(0);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [orders, setOrders] = useState<Order[]>([]);
  const [keyName, setKeyName] = useState("");
  const [expiresInDays, setExpiresInDays] = useState("");
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
        body: JSON.stringify({
          username,
          password,
          ...(endpoint === "register" ? { invite_code: inviteCode } : {}),
        }),
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
  }, [username, password, inviteCode]);

  const handleUnauthorized = useCallback((res: Response) => {
    if (res.status !== 401) return false;
    setToken("");
    localStorage.removeItem("token");
    setPage("login");
    return true;
  }, []);

  const fetchData = useCallback(async () => {
    if (!token) return;
    const headers = { Authorization: `Bearer ${token}` };
    try {
      const [balRes, planRes, keyRes, orderRes] = await Promise.all([
        fetch(`${API_BASE}/api/balance`, { headers }),
        fetch(`${API_BASE}/api/plans`),
        fetch(`${API_BASE}/api/keys`, { headers }),
        fetch(`${API_BASE}/api/orders`, { headers }),
      ]);
      if ([balRes, keyRes, orderRes].some(handleUnauthorized)) return;
      if (balRes.ok) setBalance((await balRes.json()).balance);
      if (planRes.ok) setPlans(await planRes.json());
      if (keyRes.ok) setKeys(await keyRes.json());
      if (orderRes.ok) setOrders(await orderRes.json());
    } catch {
      setError("Failed to load data");
    }
  }, [token, handleUnauthorized]);

  const createKey = useCallback(async () => {
    setError("");
    setMessage("");
    setNewKey("");
    setBusy(true);
    try {
      const res = await fetch(`${API_BASE}/api/keys`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify({ name: keyName, expires_in_days: Number(expiresInDays) || 0 }),
      });
      if (handleUnauthorized(res)) return;
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
  }, [token, keyName, expiresInDays, fetchData, handleUnauthorized]);

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
      if (handleUnauthorized(createRes)) return;
      const order = await createRes.json();
      if (!createRes.ok) {
        setError(order.error || "Failed to create order");
        return;
      }
      const payRes = await fetch(`${API_BASE}/api/orders/${order.id}/pay`, { method: "POST", headers });
      if (handleUnauthorized(payRes)) return;
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
  }, [token, fetchData, handleUnauthorized]);

  const disableKey = useCallback(async (id: number) => {
    setError("");
    setMessage("");
    setBusy(true);
    try {
      const res = await fetch(`${API_BASE}/api/keys/${id}`, {
        method: "DELETE",
        headers: { Authorization: `Bearer ${token}` },
      });
      if (handleUnauthorized(res)) return;
      const data = await res.json();
      if (!res.ok) {
        setError(data.error || "Failed to disable key");
        return;
      }
      setMessage(`Key #${id} disabled`);
      await fetchData();
    } catch {
      setError("Network error");
    } finally {
      setBusy(false);
    }
  }, [token, fetchData, handleUnauthorized]);

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
          <input style={styles.input} placeholder="Invite code (register only)" value={inviteCode} onChange={e => setInviteCode(e.target.value)} />
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
            <input
              style={{ ...styles.input, marginBottom: 0, maxWidth: 140 }}
              placeholder="Expires (days)"
              value={expiresInDays}
              onChange={e => setExpiresInDays(e.target.value)}
            />
            <button style={styles.button} onClick={createKey} disabled={busy}>Create key</button>
          </div>
          {newKey && (
            <p style={styles.newKey}>New key: <code>{newKey}</code></p>
          )}
          <table style={styles.table}>
            <thead>
              <tr><th>Name</th><th>Key</th><th>Status</th><th>Expires</th><th>Last used</th><th>Created</th><th>Actions</th></tr>
            </thead>
            <tbody>
              {keys.map(k => (
                <tr key={k.id}>
                  <td>{k.name || "-"}</td>
                  <td style={{ fontFamily: "monospace", fontSize: 12 }}>{k.key_prefix}...</td>
                  <td>{k.is_active ? "Active" : "Disabled"}</td>
                  <td>{k.expires_at ? new Date(k.expires_at).toLocaleDateString() : "Never"}</td>
                  <td>{k.last_used_at ? new Date(k.last_used_at).toLocaleString() : "Never"}</td>
                  <td>{new Date(k.created_at).toLocaleDateString()}</td>
                  <td>
                    {k.is_active && (
                      <button style={styles.smallButton} onClick={() => disableKey(k.id)} disabled={busy}>Disable</button>
                    )}
                  </td>
                </tr>
              ))}
              {keys.length === 0 && <tr><td colSpan={7}>No API keys yet</td></tr>}
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

        <section style={styles.section}>
          <h2>Orders</h2>
          <table style={styles.table}>
            <thead>
              <tr><th>ID</th><th>Plan</th><th>Amount</th><th>Status</th><th>Created</th><th>Paid</th></tr>
            </thead>
            <tbody>
              {orders.map(o => (
                <tr key={o.id}>
                  <td>{o.id}</td>
                  <td>{plans.find(p => p.id === o.plan_id)?.name || o.plan_id}</td>
                  <td>¥{(o.amount_cents / 100).toFixed(2)}</td>
                  <td>{o.status}</td>
                  <td>{new Date(o.created_at).toLocaleString()}</td>
                  <td>{o.paid_at ? new Date(o.paid_at).toLocaleString() : "-"}</td>
                </tr>
              ))}
              {orders.length === 0 && <tr><td colSpan={6}>No orders yet</td></tr>}
            </tbody>
          </table>
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
  smallButton: { padding: "4px 10px", background: "#b91c1c", color: "#fff", border: "none", borderRadius: 4, cursor: "pointer", fontSize: 12 },
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


