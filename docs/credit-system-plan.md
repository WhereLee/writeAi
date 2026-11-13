# 虚拟积分与管理员发放方案

> 本文只描述方案，不包含任何密钥或凭据。
> 2026-11-14 决定：不接支付宝，不做真实货币充值。

## 1. 结论

```text
管理员发放积分 -> 积分余额 -> 按实际 token 用量扣费
```

- 积分是 token 余额的展示单位，不引入第二套货币。
- 底层账本统一用整数 token，所有换算只在展示层做。
- 发放、扣减、消费、退回全部写流水，流水只追加，不修改、不删除。
- v1 用单管理员角色；v2 再上 RBAC 表。

## 2. 和旧方案的变化

删除：

- `plans`、`orders` 套餐和下单逻辑。
- `PayOrder` 模拟支付。
- 支付宝下单、回调、退款、对账相关代码和配置。

保留：

- 现有 token 计费、预扣、结算、失败退回。
- `balance_logs` 作为流水表，扩展关联字段。

新增：

- 管理员发放/扣减接口。
- 积分操作单、审计日志、对账任务。

## 3. 单位

配置项：

```text
TOKENS_PER_CREDIT=1000
```

含义：

```text
1 积分 = 1000 tokens
100000 tokens = 100 积分
```

规则：

- 数据库只存 tokens，不存浮点数。
- 接口入参用积分，服务端换算成 tokens 后落库。
- 展示层保留两位小数，只用于显示。
- 文案统一叫“积分”，不出现“元、人民币、提现、理财”。

## 4. 数据模型

### 4.1 用户角色（v1）

```sql
ALTER TABLE users
  ADD COLUMN role VARCHAR(16) NOT NULL DEFAULT 'user';

CREATE INDEX idx_users_role ON users(role);
```

取值：`user`、`admin`。v2 迁移到 RBAC 表时保留该列作为兼容字段。

### 4.2 积分操作单

```sql
CREATE TABLE credit_operations (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  op_no VARCHAR(40) NOT NULL,
  user_id BIGINT NOT NULL,
  kind VARCHAR(16) NOT NULL,              -- GRANT / REVOKE
  delta_tokens BIGINT NOT NULL,           -- 正数发放，负数扣减
  balance_before BIGINT NOT NULL,
  balance_after BIGINT NOT NULL,
  reason VARCHAR(255) NOT NULL,
  operator_id BIGINT NOT NULL,
  client_request_id VARCHAR(64) NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_op_no (op_no),
  UNIQUE KEY uk_operator_request (operator_id, client_request_id),
  KEY idx_user_created (user_id, created_at),
  KEY idx_operator_created (operator_id, created_at)
) ENGINE=InnoDB;
```

`uk_operator_request` 是发放接口的幂等键。同一个管理员重复提交同一个
`client_request_id`，返回原操作单，不再加钱。

### 4.3 流水表扩展

```sql
ALTER TABLE balance_logs
  ADD COLUMN reference_type VARCHAR(32) NULL,
  ADD COLUMN reference_id BIGINT NULL,
  ADD COLUMN operator_id BIGINT NULL,
  ADD COLUMN reason VARCHAR(255) NULL,
  ADD UNIQUE KEY uk_balance_reference (reference_type, reference_id, type),
  ADD UNIQUE KEY uk_balance_usage_type (usage_log_id, type),
  ADD KEY idx_user_created (user_id, created_at);
```

约定：

- 发放：`type='grant'`，`reference_type='credit_operation'`。
- 扣减：`type='revoke'`，`reference_type='credit_operation'`。
- 消费：`type='consume'`，`usage_log_id` 关联用量记录。
- 退回：`type='refund'`，`usage_log_id` 关联用量记录。
- 迁移：`type='opening'`，`reference_type='migration'`。

`uk_balance_usage_type` 保证同一条用量不会重复消费或重复退回。
MySQL 的唯一索引允许多个 `NULL`，所以发放类流水不受影响。

### 4.4 预扣占位

```sql
CREATE TABLE balance_holds (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  request_id VARCHAR(64) NOT NULL,
  user_id BIGINT NOT NULL,
  amount BIGINT NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'HELD',  -- HELD / SETTLED / RELEASED
  expires_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  settled_at TIMESTAMP NULL,
  UNIQUE KEY uk_hold_request (user_id, request_id),
  KEY idx_hold_expire (status, expires_at)
) ENGINE=InnoDB;
```

预扣不再只存在 Redis 里。每次请求先在 MySQL 落一条占位记录，进程崩了也能
由清理任务把余额退回去。

### 4.5 审计日志

```sql
CREATE TABLE admin_audit_logs (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  actor_id BIGINT NOT NULL,
  action VARCHAR(64) NOT NULL,           -- credit.grant / credit.revoke / user.role.change
  target_type VARCHAR(32) NOT NULL,      -- user / credit_operation
  target_id BIGINT NOT NULL,
  request_id VARCHAR(64) NULL,
  ip VARCHAR(64) NULL,
  user_agent VARCHAR(255) NULL,
  detail JSON NULL,                      -- before/after、金额、原因
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  KEY idx_actor_created (actor_id, created_at),
  KEY idx_target (target_type, target_id)
) ENGINE=InnoDB;
```

审计日志和业务操作在同一个事务里写入。没有审计记录的操作视为失败。

## 5. 账本原则

### 5.1 MySQL 是唯一真相

当前实现是 Redis 主导余额，定时把 Redis 覆盖回 MySQL。这个模型对发放不友好：

- 发放写了 MySQL，Redis 删除失败时，下一轮同步会把 MySQL 覆盖回旧值。
- Redis 和 MySQL 之间没有版本号，无法判断谁新谁旧。

新模型：

- 所有余额变更在 MySQL 事务里完成。
- `balances.balance` 和 `balance_logs` 在同一个事务里更新。
- Redis 只做读缓存，键 `balance:<user_id>`，TTL 5 秒，变更后主动删除。
- 删除失败不影响正确性，最多 5 秒后缓存过期，重新从 MySQL 加载。

代价是每个请求多一次 MySQL 事务。LLM 请求本身是秒级，多 2–5 毫秒可以接受。
换来的是账本可对账、可审计，不再需要 `SyncAll` 和漂移修复。

### 5.2 不变量

任何时刻，对每个用户：

```text
balances.balance = SUM(balance_logs.amount)
```

允许结算时因实际 token 超过预扣而出现负数，但负数用户不能发起新请求。
负数只可能来自结算差额，不能来自发放或扣减。

### 5.3 对账

每天跑一次：

```sql
SELECT b.user_id, b.balance, COALESCE(SUM(l.amount), 0) AS ledger
FROM balances b
LEFT JOIN balance_logs l ON l.user_id = b.user_id
GROUP BY b.user_id, b.balance
HAVING b.balance <> ledger;
```

有差异只告警，不自动改账。先查原因，再人工补一条带原因的调整流水。

## 6. 发放流程

接口：

```text
POST /api/admin/credits/grant
Authorization: Bearer <JWT>
Idempotency-Key: <client_request_id>

{
  "user_id": 12,
  "credits": 100,
  "reason": "内测额度"
}
```

事务：

```text
BEGIN
  SELECT id FROM users WHERE id = ? FOR UPDATE
    -- 不存在 -> 404

  SELECT balance FROM balances WHERE user_id = ? FOR UPDATE
    -- 不存在 -> 先插入 0

  INSERT credit_operations
    (op_no, user_id, kind='GRANT', delta_tokens=+100000,
     balance_before, balance_after, reason, operator_id, client_request_id)
    -- 唯一键冲突 -> 回滚，返回已有操作单

  UPDATE balances SET balance = balance + 100000 WHERE user_id = ?

  INSERT balance_logs
    (user_id, type='grant', amount=+100000, balance_after,
     reference_type='credit_operation', reference_id=op.id,
     operator_id, reason)

  INSERT admin_audit_logs (...)
COMMIT

DEL balance:<user_id>
```

规则：

- `credits > 0`，单次上限由 `MAX_GRANT_CREDITS_PER_OP` 控制。
- `reason` 必填，1–255 字符。
- 同一 `client_request_id` 重复提交，返回 200 和原操作单。
- v1 不禁止管理员给自己发放，但每一笔都会写审计日志；需要更严格时再加开关。
- 管理员接口限流，默认 30 次/分钟。

扣减接口 `POST /api/admin/credits/revoke` 流程相同，区别：

- `delta_tokens` 为负数。
- 事务内判断 `balance + delta_tokens >= 0`，否则返回 409，不写任何记录。
- `kind='REVOKE'`，流水 `type='revoke'`。

不做“把余额改成某个值”的接口。要纠正只能再发一笔发放或扣减，原因写清楚。
这样账本永远能解释每一分钱的来源。

## 7. 扣费流程

### 7.1 预扣

```text
BEGIN
  SELECT balance FROM balances WHERE user_id = ? FOR UPDATE
  if balance < estimated: ROLLBACK -> 402
  UPDATE balances SET balance = balance - estimated
  INSERT balance_holds (request_id, user_id, amount, status='HELD',
                        expires_at=NOW()+1h)
COMMIT
```

`request_id` 取 `X-Request-Id`。重复请求命中 `uk_hold_request` 时直接返回冲突，
不重复扣款。

### 7.2 结算

```text
BEGIN
  SELECT balance FROM balances WHERE user_id = ? FOR UPDATE
  SELECT amount, status FROM balance_holds
    WHERE user_id = ? AND request_id = ? FOR UPDATE
  if status != 'HELD': return            -- 幂等

  delta = held_amount - actual_tokens
  UPDATE balances SET balance = balance + delta
  UPDATE balance_holds SET status='SETTLED', settled_at=NOW()
  INSERT balance_logs (type='consume', amount=-actual_tokens, usage_log_id, ...)
COMMIT
```

`uk_balance_usage_type` 保证同一条用量只结算一次。

### 7.3 失败退回

上游报错、客户端断开、超时：

```text
BEGIN
  SELECT balance FOR UPDATE
  SELECT hold FOR UPDATE
  if status != 'HELD': return
  UPDATE balances SET balance = balance + held_amount
  UPDATE balance_holds SET status='RELEASED'
  INSERT balance_logs (type='refund', amount=+held_amount, usage_log_id, ...)
COMMIT
```

### 7.4 占位清理

每 5 分钟扫一次 `status='HELD' AND expires_at < NOW()` 的记录，按 7.3 退回。
这样进程崩溃、容器重启不会把用户的余额永久扣住。

### 7.5 负数策略

- 预扣按“消息估算 + `max_tokens`”计算；请求带 `max_tokens` 时优先用它。
- 实际超过预扣时允许余额为负，但记录 `overdrawn` 标记并告警。
- 负数用户下一次预扣直接返回 402，直到管理员补发。
- 不允许通过发放或扣减制造负数。

## 8. 权限

### 8.1 v1

- `users.role`：`user` / `admin`。
- 中间件 `RequireRole("admin")` 保护 `/api/admin/*`。
- 首个管理员用 SQL 手动指定，不自动创建默认密码：

```sql
UPDATE users SET role = 'admin' WHERE username = '<管理员用户名>';
```

### 8.2 v2 RBAC

```sql
CREATE TABLE roles (id BIGINT PRIMARY KEY AUTO_INCREMENT, code VARCHAR(32) UNIQUE, name VARCHAR(64));
CREATE TABLE permissions (id BIGINT PRIMARY KEY AUTO_INCREMENT, code VARCHAR(64) UNIQUE, name VARCHAR(64));
CREATE TABLE role_permissions (role_id BIGINT, permission_id BIGINT, PRIMARY KEY (role_id, permission_id));
CREATE TABLE user_roles (user_id BIGINT, role_id BIGINT, granted_by BIGINT, granted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (user_id, role_id));
```

预置角色：

- `user`：普通用户。
- `credit_admin`：发放、扣减、查看流水。
- `auditor`：只读审计和流水。

预置权限：

- `credit.grant`
- `credit.revoke`
- `credit.read`
- `audit.read`
- `user.read`

迁移时把 `users.role='admin'` 映射到 `credit_admin` + `auditor`。

## 9. API

```text
POST /api/admin/credits/grant
POST /api/admin/credits/revoke
GET  /api/admin/credits/operations?user_id=&operator_id=&from=&to=&page=
GET  /api/admin/users?q=&page=
GET  /api/admin/audit-logs?actor_id=&action=&from=&to=&page=

GET  /api/credits/ledger?type=&from=&to=&page=      # 当前用户自己的流水
GET  /api/balance
```

`GET /api/balance` 返回：

```json
{
  "user_id": 12,
  "tokens": 250000,
  "credits": "250.00"
}
```

`credits` 是字符串，避免前端浮点误差。

## 10. 前端

用户侧：

- 顶部显示积分余额，悬浮显示 tokens。
- 积分流水页：时间、类型、变动、变动后余额、原因。
- 用量页：模型、prompt/completion/total tokens、折合积分、状态码。

管理侧：

- 用户搜索。
- 发放/扣减弹窗：用户、积分、原因、二次确认。
- 大额发放要求输入用户名确认。
- 操作记录、审计日志。

删除：

- 套餐页、购买按钮、订单页。

## 11. 迁移步骤

1. 备份 MySQL 和 Redis。
2. 执行表结构变更。
3. 应用启动时自动补开账流水。对每个用户计算：

```text
delta = balances.balance - SUM(旧 balance_logs.amount)
```

写入一条 `type='opening'`、`reference_type='migration'` 的流水，
`amount=delta`，每个用户只写一次。这样
`SUM(balance_logs.amount) = balances.balance` 从迁移时刻起成立。
旧账本有缺失时 `delta` 可能为负，这是对历史差异的显式修正，不是新漏洞。

4. 校验 `SUM(balance_logs.amount) = balances.balance`。
5. 切换计费引擎到 MySQL 账本。
6. 删除 `plans`、`orders` 相关路由和前端页面。
7. 部署后跑一次全量对账。

## 12. 测试清单

- 同一 `client_request_id` 并发发放 10 次，只加一次钱。
- 两个管理员同时给同一用户发放，余额等于两笔之和。
- 扣减超过余额返回 409，且不写流水。
- 非管理员访问 `/api/admin/*` 返回 403。
- 每次发放都有对应的审计日志。
- 同一 `usage_log_id` 重复结算，只扣一次。
- 上游失败退回后重试，不会重复退款。
- 预扣占位过期后只退回一次。
- 发放后 `/api/balance` 立即返回新余额。
- 余额为负时新请求返回 402。
- 全量对账无差异。
- 50 并发发放 + 100 并发请求，账本无漂移。

## 13. 风险

- 管理员误发：单次上限、二次确认、可反向操作，不能改历史流水。
- 积分通胀：每日发放报表，超过阈值告警。
- 账本迁移：开账流水必须先生成并校验，再切引擎。
- 性能：MySQL 事务比 Redis 慢，先压测；如果确实成为瓶颈，再加只读缓存，
  但账本仍然只写 MySQL。
- 合规文案：积分不可提现、不可转让、无现金价值。

## 14. 暂不做

- 支付宝、微信支付。
- 积分过期和按批次 FIFO 扣减。
- 多级审批流。
- 完整 RBAC 管理界面。

这些都可以在账本不变的前提下后续加。
