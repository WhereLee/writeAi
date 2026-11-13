import React, { useCallback, useEffect, useState } from "react";

const API_BASE = import.meta.env.VITE_API_BASE ?? "";

interface Me {
  user_id: number;
  username: string;
  role: string;
  tokens: number;
  credits: string;
  tokens_per_credit: number;
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

interface LedgerEntry {
  id: number;
  type: string;
  amount: number;
  balance_after: number;
  reason: string | null;
  created_at: string;
}

interface UsageEntry {
  id: number;
  model: string;
  prompt_tokens: number;
  output_tokens: number;
  total_tokens: number;
  latency_ms: number;
  status_code: number;
  created_at: string;
}

interface CreditOperation {
  id: number;
  op_no: string;
  user_id: number;
  kind: string;
  delta_tokens: number;
  balance_before: number;
  balance_after: number;
  reason: string;
  operator_id: number;
  created_at: string;
}

interface AdminUser {
  id: number;
  username: string;
  role: string;
  tokens: number;
  credits: string;
  created_at: string;
}

interface AuditEntry {
  id: number;
  actor_id: number;
  action: string;
  target_id: number;
  ip: string | null;
  detail: unknown;
  created_at: string;
}

type Tab = "keys" | "ledger" | "usage" | "admin";

const typeLabel: Record<string, string> = {
  grant: "发放",
  revoke: "扣减",
  consume: "消费",
  refund: "退回",
  opening: "开账",
};

function newRequestId(): string {
  const cryptoObject = globalThis.crypto;
  if (cryptoObject && typeof cryptoObject.randomUUID === "function") {
    return cryptoObject.randomUUID();
  }
  return `req-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function App() {
  const [token, setToken] = useState<string>(localStorage.getItem("token") || "");
  const [me, setMe] = useState<Me | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [inviteCode, setInviteCode] = useState("");
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [ledger, setLedger] = useState<LedgerEntry[]>([]);
  const [usage, setUsage] = useState<UsageEntry[]>([]);
  const [adminUsers, setAdminUsers] = useState<AdminUser[]>([]);
  const [operations, setOperations] = useState<CreditOperation[]>([]);
  const [auditLogs, setAuditLogs] = useState<AuditEntry[]>([]);
  const [keyName, setKeyName] = useState("");
  const [expiresInDays, setExpiresInDays] = useState("");
  const [newKey, setNewKey] = useState("");
  const [grantUserID, setGrantUserID] = useState("");
  const [grantCredits, setGrantCredits] = useState("100");
  const [grantReason, setGrantReason] = useState("");
  const [userQuery, setUserQuery] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState<Tab>("keys");
  const [page, setPage] = useState<"login" | "dashboard">(token ? "dashboard" : "login");

  const handleUnauthorized = useCallback((res: Response) => {
    if (res.status !== 401) return false;
    setToken("");
    setMe(null);
    localStorage.removeItem("token");
    setPage("login");
    return true;
  }, []);

  const fetchAdminData = useCallback(async () => {
    if (!token) return;
    const headers = { Authorization: `Bearer ${token}` };
    try {
      const [usersRes, opsRes, auditRes] = await Promise.all([
        fetch(`${API_BASE}/api/admin/users?limit=20`, { headers }),
        fetch(`${API_BASE}/api/admin/credits/operations?limit=20`, { headers }),
        fetch(`${API_BASE}/api/admin/audit-logs?limit=20`, { headers }),
      ]);
      if ([usersRes, opsRes, auditRes].some(handleUnauthorized)) return;
      if (usersRes.ok) setAdminUsers(await usersRes.json());
      if (opsRes.ok) setOperations(await opsRes.json());
      if (auditRes.ok) setAuditLogs(await auditRes.json());
    } catch {
      setError("加载管理数据失败");
    }
  }, [token, handleUnauthorized]);

  const fetchAll = useCallback(async () => {
    if (!token) return;
    const headers = { Authorization: `Bearer ${token}` };
    try {
      const meRes = await fetch(`${API_BASE}/api/me`, { headers });
      if (handleUnauthorized(meRes)) return;
      if (!meRes.ok) {
        setError("加载用户信息失败");
        return;
      }
      const meData: Me = await meRes.json();
      setMe(meData);

      const [keyRes, ledgerRes, usageRes] = await Promise.all([
        fetch(`${API_BASE}/api/keys`, { headers }),
        fetch(`${API_BASE}/api/credits/ledger?limit=50`, { headers }),
        fetch(`${API_BASE}/api/usage?limit=50`, { headers }),
      ]);
      if ([keyRes, ledgerRes, usageRes].some(handleUnauthorized)) return;
      if (keyRes.ok) setKeys(await keyRes.json());
      if (ledgerRes.ok) setLedger(await ledgerRes.json());
      if (usageRes.ok) setUsage(await usageRes.json());
      if (meData.role === "admin") {
        await fetchAdminData();
      }
    } catch {
      setError("网络错误");
    }
  }, [token, handleUnauthorized, fetchAdminData]);

  useEffect(() => {
    if (token) {
      setPage("dashboard");
      fetchAll();
    }
  }, [token, fetchAll]);

  useEffect(() => {
    if (tab === "admin" && me?.role !== "admin") {
      setTab("keys");
    }
  }, [tab, me]);

  const auth = useCallback(async (endpoint: "login" | "register") => {
    setError("");
    setMessage("");
    setBusy(true);
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
        setError(data.error || "操作失败");
        return;
      }
      if (data.token) {
        setToken(data.token);
        localStorage.setItem("token", data.token);
      }
    } catch {
      setError("网络错误");
    } finally {
      setBusy(false);
    }
  }, [username, password, inviteCode]);

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
        setError(data.error || "创建失败");
        return;
      }
      setNewKey(data.key);
      setKeyName("");
      await fetchAll();
    } catch {
      setError("网络错误");
    } finally {
      setBusy(false);
    }
  }, [token, keyName, expiresInDays, fetchAll, handleUnauthorized]);

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
        setError(data.error || "停用失败");
        return;
      }
      setMessage(`Key #${id} 已停用`);
      await fetchAll();
    } catch {
      setError("网络错误");
    } finally {
      setBusy(false);
    }
  }, [token, fetchAll, handleUnauthorized]);

  const submitCredit = useCallback(async (revoke: boolean) => {
    const userID = Number(grantUserID);
    const credits = Number(grantCredits);
    if (!userID || !credits || credits <= 0 || !grantReason.trim()) {
      setError("请填写用户 ID、积分和原因");
      return;
    }
    const verb = revoke ? "扣减" : "发放";
    if (!window.confirm(`确认给用户 #${userID} ${verb} ${credits} 积分？`)) return;
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const res = await fetch(`${API_BASE}/api/admin/credits/${revoke ? "revoke" : "grant"}`, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
          "Idempotency-Key": newRequestId(),
        },
        body: JSON.stringify({ user_id: userID, credits, reason: grantReason.trim() }),
      });
      if (handleUnauthorized(res)) return;
      const data = await res.json();
      if (!res.ok) {
        setError(data.error || "操作失败");
        return;
      }
      setMessage(`用户 #${data.user_id} ${verb} ${data.delta_credits} 积分，当前 ${data.balance_credits} 积分`);
      setGrantReason("");
      await fetchAll();
    } catch {
      setError("网络错误");
    } finally {
      setBusy(false);
    }
  }, [token, grantUserID, grantCredits, grantReason, fetchAll, handleUnauthorized]);

  const searchUsers = useCallback(async () => {
    if (!token) return;
    setError("");
    try {
      const res = await fetch(`${API_BASE}/api/admin/users?q=${encodeURIComponent(userQuery)}&limit=50`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (handleUnauthorized(res)) return;
      const data = await res.json();
      if (!res.ok) {
        setError(data.error || "搜索失败");
        return;
      }
      setAdminUsers(data);
    } catch {
      setError("网络错误");
    }
  }, [token, userQuery, handleUnauthorized]);

  const logout = () => {
    setToken("");
    setMe(null);
    localStorage.removeItem("token");
    setPage("login");
  };

  const tokensPerCredit = me?.tokens_per_credit || 1000;
  const creditsOf = (tokens: number) => (tokens / tokensPerCredit).toFixed(2);
  const formatTime = (value: string) => new Date(value).toLocaleString();

  if (page === "login") {
    return (
      <div style={styles.container}>
        <div style={styles.card}>
          <h1 style={styles.title}>LLM API Gateway</h1>
          <input style={styles.input} placeholder="Username" value={username} onChange={e => setUsername(e.target.value)} />
          <input style={styles.input} type="password" placeholder="Password" value={password} onChange={e => setPassword(e.target.value)} />
          <input style={styles.input} placeholder="Invite code (register only)" value={inviteCode} onChange={e => setInviteCode(e.target.value)} />
          <div style={{ display: "flex", gap: 8 }}>
            <button style={styles.button} onClick={() => auth("login")} disabled={busy}>Login</button>
            <button style={{ ...styles.button, background: "#2563eb" }} onClick={() => auth("register")} disabled={busy}>Register</button>
          </div>
          {error && <p style={styles.error}>{error}</p>}
        </div>
      </div>
    );
  }

  const tabs: { id: Tab; label: string }[] = [
    { id: "keys", label: "API Keys" },
    { id: "ledger", label: "积分流水" },
    { id: "usage", label: "用量" },
    ...(me?.role === "admin" ? [{ id: "admin" as Tab, label: "管理" }] : []),
  ];

  return (
    <div style={styles.container}>
      <nav style={styles.nav}>
        <span style={styles.navTitle}>LLM API Gateway</span>
        <span style={{ marginLeft: "auto", marginRight: 16 }}>
          {me ? `${me.credits} 积分` : "-"} <span style={styles.muted}>({me ? me.tokens.toLocaleString() : "-"} tokens)</span>
        </span>
        {me?.role === "admin" && <span style={styles.badge}>管理员</span>}
        <button style={styles.navButton} onClick={logout}>Logout</button>
      </nav>
      <div style={styles.content}>
        {error && <p style={styles.error}>{error}</p>}
        {message && <p style={styles.success}>{message}</p>}

        <div style={styles.tabBar}>
          {tabs.map(item => (
            <button
              key={item.id}
              style={{ ...styles.tab, ...(tab === item.id ? styles.tabActive : {}) }}
              onClick={() => setTab(item.id)}
            >
              {item.label}
            </button>
          ))}
        </div>

        {tab === "keys" && (
          <section style={styles.section}>
            <h2>API Keys</h2>
            <div style={{ display: "flex", gap: 8, marginBottom: 12 }}>
              <input style={{ ...styles.input, marginBottom: 0 }} placeholder="Key name" value={keyName} onChange={e => setKeyName(e.target.value)} />
              <input style={{ ...styles.input, marginBottom: 0, maxWidth: 140 }} placeholder="Expires (days)" value={expiresInDays} onChange={e => setExpiresInDays(e.target.value)} />
              <button style={styles.button} onClick={createKey} disabled={busy}>Create key</button>
            </div>
            {newKey && <p style={styles.newKey}>New key: <code>{newKey}</code></p>}
            <table style={styles.table}>
              <thead>
                <tr><th>Name</th><th>Key</th><th>Status</th><th>Expires</th><th>Last used</th><th>Actions</th></tr>
              </thead>
              <tbody>
                {keys.map(k => (
                  <tr key={k.id}>
                    <td>{k.name || "-"}</td>
                    <td style={styles.mono}>{k.key_prefix}...</td>
                    <td>{k.is_active ? "Active" : "Disabled"}</td>
                    <td>{k.expires_at ? formatTime(k.expires_at) : "Never"}</td>
                    <td>{k.last_used_at ? formatTime(k.last_used_at) : "Never"}</td>
                    <td>{k.is_active && <button style={styles.smallButton} onClick={() => disableKey(k.id)} disabled={busy}>Disable</button>}</td>
                  </tr>
                ))}
                {keys.length === 0 && <tr><td colSpan={6}>No API keys yet</td></tr>}
              </tbody>
            </table>
          </section>
        )}

        {tab === "ledger" && (
          <section style={styles.section}>
            <h2>积分流水</h2>
            <table style={styles.table}>
              <thead>
                <tr><th>时间</th><th>类型</th><th>变动</th><th>变动后</th><th>原因</th></tr>
              </thead>
              <tbody>
                {ledger.map(row => (
                  <tr key={row.id}>
                    <td>{formatTime(row.created_at)}</td>
                    <td>{typeLabel[row.type] || row.type}</td>
                    <td style={{ color: row.amount >= 0 ? "#15803d" : "#b91c1c" }}>{row.amount >= 0 ? "+" : ""}{creditsOf(row.amount)}</td>
                    <td>{creditsOf(row.balance_after)}</td>
                    <td>{row.reason || "-"}</td>
                  </tr>
                ))}
                {ledger.length === 0 && <tr><td colSpan={5}>暂无流水</td></tr>}
              </tbody>
            </table>
          </section>
        )}

        {tab === "usage" && (
          <section style={styles.section}>
            <h2>用量</h2>
            <table style={styles.table}>
              <thead>
                <tr><th>时间</th><th>模型</th><th>Prompt</th><th>Completion</th><th>Total</th><th>折合积分</th><th>耗时</th><th>状态</th></tr>
              </thead>
              <tbody>
                {usage.map(row => (
                  <tr key={row.id}>
                    <td>{formatTime(row.created_at)}</td>
                    <td>{row.model}</td>
                    <td>{row.prompt_tokens.toLocaleString()}</td>
                    <td>{row.output_tokens.toLocaleString()}</td>
                    <td>{row.total_tokens.toLocaleString()}</td>
                    <td>{creditsOf(row.total_tokens)}</td>
                    <td>{row.latency_ms} ms</td>
                    <td>{row.status_code}</td>
                  </tr>
                ))}
                {usage.length === 0 && <tr><td colSpan={8}>暂无用量</td></tr>}
              </tbody>
            </table>
          </section>
        )}

        {tab === "admin" && me?.role === "admin" && (
          <>
            <section style={styles.section}>
              <h2>发放 / 扣减积分</h2>
              <div style={styles.formGrid}>
                <label style={styles.field}>
                  <span style={styles.label}>用户 ID</span>
                  <input style={styles.input} value={grantUserID} onChange={e => setGrantUserID(e.target.value)} />
                </label>
                <label style={styles.field}>
                  <span style={styles.label}>积分</span>
                  <input style={styles.input} value={grantCredits} onChange={e => setGrantCredits(e.target.value)} />
                </label>
                <label style={{ ...styles.field, gridColumn: "1 / -1" }}>
                  <span style={styles.label}>原因</span>
                  <input style={styles.input} placeholder="例如：内测额度" value={grantReason} onChange={e => setGrantReason(e.target.value)} />
                </label>
              </div>
              <div style={{ display: "flex", gap: 8 }}>
                <button style={styles.button} onClick={() => submitCredit(false)} disabled={busy}>发放</button>
                <button style={styles.dangerButton} onClick={() => submitCredit(true)} disabled={busy}>扣减</button>
              </div>
            </section>

            <section style={styles.section}>
              <h2>用户</h2>
              <div style={{ display: "flex", gap: 8, marginBottom: 12 }}>
                <input style={{ ...styles.input, marginBottom: 0 }} placeholder="用户名搜索" value={userQuery} onChange={e => setUserQuery(e.target.value)} />
                <button style={styles.button} onClick={searchUsers}>搜索</button>
              </div>
              <table style={styles.table}>
                <thead>
                  <tr><th>ID</th><th>用户名</th><th>角色</th><th>积分</th><th>Tokens</th><th>注册时间</th></tr>
                </thead>
                <tbody>
                  {adminUsers.map(u => (
                    <tr key={u.id}>
                      <td>{u.id}</td>
                      <td>{u.username}</td>
                      <td>{u.role}</td>
                      <td>{u.credits}</td>
                      <td>{u.tokens.toLocaleString()}</td>
                      <td>{formatTime(u.created_at)}</td>
                    </tr>
                  ))}
                  {adminUsers.length === 0 && <tr><td colSpan={6}>暂无用户</td></tr>}
                </tbody>
              </table>
            </section>

            <section style={styles.section}>
              <h2>最近操作</h2>
              <table style={styles.table}>
                <thead>
                  <tr><th>时间</th><th>操作号</th><th>用户</th><th>类型</th><th>变动</th><th>变动后</th><th>原因</th><th>操作人</th></tr>
                </thead>
                <tbody>
                  {operations.map(op => (
                    <tr key={op.id}>
                      <td>{formatTime(op.created_at)}</td>
                      <td style={styles.mono}>{op.op_no}</td>
                      <td>{op.user_id}</td>
                      <td>{typeLabel[op.kind.toLowerCase()] || op.kind}</td>
                      <td>{creditsOf(op.delta_tokens)}</td>
                      <td>{creditsOf(op.balance_after)}</td>
                      <td>{op.reason}</td>
                      <td>{op.operator_id}</td>
                    </tr>
                  ))}
                  {operations.length === 0 && <tr><td colSpan={8}>暂无操作</td></tr>}
                </tbody>
              </table>
            </section>

            <section style={styles.section}>
              <h2>审计日志</h2>
              <table style={styles.table}>
                <thead>
                  <tr><th>时间</th><th>操作人</th><th>动作</th><th>目标用户</th><th>IP</th></tr>
                </thead>
                <tbody>
                  {auditLogs.map(row => (
                    <tr key={row.id}>
                      <td>{formatTime(row.created_at)}</td>
                      <td>{row.actor_id}</td>
                      <td>{row.action}</td>
                      <td>{row.target_id}</td>
                      <td>{row.ip || "-"}</td>
                    </tr>
                  ))}
                  {auditLogs.length === 0 && <tr><td colSpan={5}>暂无审计记录</td></tr>}
                </tbody>
              </table>
            </section>
          </>
        )}

        <p style={styles.note}>积分仅用于本站 token 消费，不可提现、不可转让、无现金价值。</p>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { minHeight: "100vh", background: "#f8fafc", fontFamily: "system-ui, sans-serif" },
  card: { maxWidth: 400, margin: "100px auto", padding: 32, background: "#fff", borderRadius: 12, boxShadow: "0 4px 6px rgba(0,0,0,0.1)" },
  title: { textAlign: "center", marginBottom: 24, color: "#1e293b" },
  input: { width: "100%", padding: "10px 12px", marginBottom: 12, border: "1px solid #d1d5db", borderRadius: 6, fontSize: 14, boxSizing: "border-box" },
  button: { padding: "10px 20px", background: "#1e40af", color: "#fff", border: "none", borderRadius: 6, cursor: "pointer", fontSize: 14 },
  dangerButton: { padding: "10px 20px", background: "#b91c1c", color: "#fff", border: "none", borderRadius: 6, cursor: "pointer", fontSize: 14 },
  smallButton: { padding: "4px 10px", background: "#b91c1c", color: "#fff", border: "none", borderRadius: 4, cursor: "pointer", fontSize: 12 },
  navButton: { padding: "6px 16px", background: "#ef4444", color: "#fff", border: "none", borderRadius: 6, cursor: "pointer", marginLeft: 12 },
  error: { color: "#dc2626", marginTop: 12, fontSize: 14 },
  success: { color: "#15803d", marginTop: 12, fontSize: 14 },
  newKey: { background: "#ecfdf5", border: "1px solid #86efac", padding: 12, borderRadius: 6, wordBreak: "break-all" },
  note: { color: "#64748b", fontSize: 13, marginTop: 24 },
  muted: { color: "#94a3b8", fontSize: 13 },
  nav: { display: "flex", alignItems: "center", padding: "12px 24px", background: "#1e293b", color: "#fff" },
  navTitle: { fontWeight: 600, fontSize: 18 },
  badge: { background: "#f59e0b", color: "#1e293b", borderRadius: 4, padding: "2px 8px", fontSize: 12, fontWeight: 600 },
  content: { maxWidth: 1100, margin: "0 auto", padding: 24 },
  tabBar: { display: "flex", gap: 4, marginBottom: 20, borderBottom: "1px solid #e2e8f0" },
  tab: { padding: "10px 18px", background: "transparent", border: "none", borderBottom: "2px solid transparent", cursor: "pointer", fontSize: 14, color: "#64748b" },
  tabActive: { color: "#1e40af", borderBottom: "2px solid #1e40af", fontWeight: 600 },
  section: { marginBottom: 32 },
  table: { width: "100%", borderCollapse: "collapse", background: "#fff", borderRadius: 8, overflow: "hidden", boxShadow: "0 1px 3px rgba(0,0,0,0.1)" },
  mono: { fontFamily: "monospace", fontSize: 12 },
  formGrid: { display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12, marginBottom: 12 },
  field: { display: "flex", flexDirection: "column" },
  label: { fontSize: 13, color: "#475569", marginBottom: 4 },
};

export default App;
