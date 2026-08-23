# Bug 复现说明

## Bug 是什么
方案评估与流转的版本/状态守卫在事务内被去掉：`BeginEvaluation`、`FinishEvaluation`、`Transition` 只按 id 更新，服务层流转前也不校验版本号，并发评估/审批会产生覆盖丢失更新。

## 如何触发
1. 创建草稿方案（POST /api/v1/scenarios），记录 version；
2. 用两个并发请求同时 POST /api/v1/scenarios/:id/evaluate（同一 version）；
3. 两个请求都会成功返回，方案 version 与审计事件翻倍；
4. 对已评估方案用旧 version 再评估或流转也会成功覆盖。

## 错误信息
无显式报错；错误表现为：并发评估两请求均 200（应一个 409）、旧版本流转 200（应 409）、最终 version 与审计条数错乱。
