# Bug 复现说明

## Bug 是什么
设备负载零值路径未防护：批量校验对无偏好热区的负载直接解引用指针 panic；功率为 0 时热功率比除出 +Inf；placed 负载被当成可再规划；热功率比未超 15% 也被判无效。

## 如何触发
1. 创建不带 preferred_zone_id 的 ready 负载，POST /api/v1/loads/validate → nil 指针 panic；
2. 创建 PowerKW=0 负载（直接入库）→ 详情 heat_ratio 变 +Inf；
3. 创建 placed 负载 → 布局评估仍会分配机柜；
4. 创建热功率比 1.1 的负载 → 批量校验误判无效。

## 错误信息
```text
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x2 addr=0x0]
```
