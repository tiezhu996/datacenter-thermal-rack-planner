# Bug 复现说明

## Bug 是什么
查询/修改不存在的负载时，仓储用 `%v` 断链、handler 吞成 internal error、web 分类器按字符串精确匹配，本应 404 的资源被误判成 500。

## 如何触发
1. 登录后 GET /api/v1/loads/9999（不存在的 id）；
2. 或 PUT /api/v1/loads/9999 修改不存在的负载。

## 错误信息
```json
{"error":{"code":"INTERNAL_ERROR","message":"internal service error"}}
```
（应返回 404 NOT_FOUND）
