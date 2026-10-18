# 用户 itcode 重复问题修复

## 问题描述

创建用户时，如果 itcode 已存在，会返回原始的 SQLite 错误：
```
constraint failed: UNIQUE constraint failed: users.itcode (2067)
```

这个错误信息对用户不友好，且前端无法识别是重复 itcode 导致的失败。

## 修复内容

### 1. 数据库层 (internal/db/user.go)

- **新增 `ErrDuplicateItcode` 错误类型**：用于标识 itcode 重复
- **新增 `NormalizeItcode` 函数**：
  - 去除首尾空格
  - 去除 `@lenovo.com` 后缀（如果存在）
  - 避免 `" zhangq5 "` 和 `"zhangq5"` 被视为不同用户
- **新增 `isUniqueItcodeViolation` 函数**：检测 SQLite UNIQUE 约束违反
- **更新 `CreateUser`**：
  - 创建前先规范化 itcode
  - 检测到 UNIQUE 约束违反时返回 `ErrDuplicateItcode`
- **更新 `UpdateUserItcode`**：
  - 更新前先规范化 itcode
  - 检测到 UNIQUE 约束违反时返回 `ErrDuplicateItcode`

### 2. API 处理层 (internal/handler/user.go)

- **导入 `errors` 包**：用于 `errors.Is` 检查
- **更新 `CreateUser` 处理器（幂等语义）**：
  - 使用 `errors.Is` 检测 `ErrDuplicateItcode`
  - 如果用户已存在，返回 HTTP 200 OK（而非 409 Conflict）
  - 返回现有用户数据（不覆盖现有配置，如 quota、status、role）
  - 返回结构：`{"user": {...}, "existed": true, "message": "用户已存在，无需重复添加"}`
  - 新创建时返回：`{"user": {...}, "existed": false, "message": "添加成功"}`
- **更新 `UpdateItcode` 处理器**：
  - 使用 `errors.Is` 检测 `ErrDuplicateItcode`
  - 返回 HTTP 409 Conflict 状态码（重命名冲突不是幂等场景）
  - 返回中文友好提示："该 itcode 已存在，请使用其他 itcode"
  - 返回规范化后的 itcode

### 3. 前端 (web/src/pages/AdminUsersPage.tsx)

- **新增 `successMsg` 状态**：用于显示成功提示（绿色）
- **更新 `handleCreate` 函数**：
  - 检查响应中的 `existed` 字段
  - 如果用户已存在，显示绿色提示信息 2 秒后自动关闭弹窗
  - 新创建用户时立即关闭弹窗
- **更新取消按钮**：清除 error 和 successMsg 状态
- **新增成功提示渲染**：`{successMsg && <p className="text-sm text-green-600">{successMsg}</p>}`

### 4. 测试覆盖

**数据库层测试** (internal/db/user_duplicate_test.go)：
- `TestCreateUser_DuplicateItcode`：验证重复 itcode 被正确拒绝
- `TestCreateUser_NormalizeItcode`：验证 itcode 规范化逻辑
- `TestCreateUser_NormalizePreventsCollision`：验证规范化后可以防止变体重复
- `TestUpdateUserItcode_Duplicate`：验证更新 itcode 时的重复检测

**Handler 层测试** (internal/handler/user_test.go)：
- `TestCreateUser_Idempotent`：验证 API 幂等性（重复创建返回现有用户）
- `TestCreateUser_NormalizedDuplicate`：验证规范化后的幂等行为

所有测试通过 ✓

## 影响范围

### 修改的文件
- `internal/db/user.go`：数据库层逻辑
- `internal/handler/user.go`：API 处理器（幂等语义）
- `web/src/pages/AdminUsersPage.tsx`：前端用户创建界面
- `internal/db/user_duplicate_test.go`：新增数据库测试
- `internal/handler/user_test.go`：新增 handler 测试

### 调用链
根据代码搜索，`CreateUser` 被以下位置调用：
1. `cmd/server/main.go:435` - POST `/admin/api/users` 路由（本次修改）
2. `internal/handler/auth.go:148` - 自动创建用户（登录时，受益于规范化）
3. `internal/db/user.go:305,345` - TransferAPIKey 和 EnsureAdmin（受益于规范化）

所有调用点都会受益于改进的错误处理和 itcode 规范化。

### API 响应变化

**创建用户 (POST /admin/api/users)**

之前（失败时）:
```json
HTTP 500
{"error": "create user: constraint failed: UNIQUE constraint failed: users.itcode (2067)"}
```

现在（用户已存在）:
```json
HTTP 200
{
  "user": {"id": 1, "itcode": "zhangq5", "name": "张三", ...},
  "existed": true,
  "message": "用户已存在，无需重复添加"
}
```

现在（新建成功）:
```json
HTTP 201
{
  "user": {"id": 1, "itcode": "zhangq5", "name": "张三", ...},
  "existed": false,
  "message": "添加成功"
}
```

**更新 itcode (PUT /admin/api/users/:id/itcode)**

之前（失败时）:
```json
HTTP 500
{"error": "..."}
```

现在（失败时）:
```json
HTTP 409
{"error": "该 itcode 已存在，请使用其他 itcode"}
```

## 幂等语义说明

**为什么用户创建是幂等的？**

1. **避免重复添加**：管理员不需要先搜索用户是否存在再决定是否添加
2. **简化流程**：直接输入 itcode，系统自动处理存在/不存在两种情况
3. **保护现有数据**：已存在用户的 quota、status、role 等配置不会被覆盖
4. **友好体验**：绿色提示而非红色错误，明确告知"已存在"而非"失败"

**为什么重命名不是幂等的？**

重命名 itcode 到已存在的值是真正的冲突——用户期望修改成功，失败时需要明确提示选择其他 itcode。

## 兼容性

- **向后兼容**：不改变成功场景的行为
- **前端兼容**：前端已有错误处理逻辑，新增 `existed` 字段检查增强体验
- **HTTP 状态码**：
  - 创建用户：从 500 改为 200（已存在）或 201（新建）
  - 重命名：从 500 改为 409，更符合 REST 规范

## 验证步骤

```bash
# 1. 运行数据库层测试
go test -v ./internal/db -run TestCreateUser
go test -v ./internal/db -run TestUpdateUserItcode

# 2. 运行 handler 层测试
go test -v ./internal/handler -run TestCreateUser

# 3. 构建验证
go build ./cmd/server

# 4. 手动测试（可选）
# - 通过管理界面创建相同 itcode 的用户
# - 验证显示绿色提示"用户已存在，无需重复添加"
# - 验证用户列表刷新
```

## 后续改进建议

1. **大小写敏感性**：当前 SQLite TEXT 比较区分大小写，`"ABC"` 和 `"abc"` 被视为不同。如果需要不区分大小写，可以：
   - 在 `NormalizeItcode` 中添加 `strings.ToLower()`
   - 或创建 COLLATE NOCASE 索引

2. **前端预检**：在创建/更新前调用搜索 API 检查 itcode 是否已存在，提供实时反馈

3. **审计日志**：记录重复 itcode 的尝试，用于分析是否有误用模式

