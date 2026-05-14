Pulsar 恢复了。现在重启 message 服务就行：

原因：Pulsar 数据卷损坏（docker compose down 没加 -v，旧 ledger 数据不完整），message 服务连不上，重连时 Pulsar 客户端内部残留了僵尸
consumer ID，导致无限 Consumer that failed is already present on the connection 循环。

解决：
docker compose down -v 彻底清掉数据卷 → 
docker compose up -d 重建 → 重启 message 服务。

▎ 以后停依赖直接用 docker compose down -v，避免残留损坏的 ledger 数据。