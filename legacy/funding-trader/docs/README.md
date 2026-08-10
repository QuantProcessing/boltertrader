# 自费套利 V1 文档

- [产品文档](./PRODUCT_V1.md)：产品范围、角色权限、指标定义、核心流程与验收标准。
- [API 对接文档](./API_V1.md)：前后端接口契约、请求响应示例、状态映射与安全要求。
- [运行手册](./OPERATIONS.md)：部署拓扑、模式切换、失败关闭写入门、切换回滚、验证与监控。
- [Test-mode Gateway 验收记录](./TEST_MODE_GATEWAY_ACCEPTANCE.md)：Test 模式下真实 Gateway 能力矩阵、已执行测试、私有写入门槛与外部阻断。

当前仓库包含可运行的 Go 后端和已接入真实 API/SSE 的前端。Gateway 仍是
交易所账户、凭证和所有私有写入的唯一所有者。
