# alert-bridge —— NOX 告警→AI 分诊桥（本机件）

设计稿 §8.2 本机桥票交付面：

- `bridge.py` —— HTTP listener(127.0.0.1:8099,X-Bridge-Token 鉴权）+内存
  串行队列（上限 3)+spawner 串行 `claude -p`（权限收窄--allowedTools
  白名单：只读件+docker/git 读命令族+写面仅草案/报告/收件箱+飞书端点；
  禁 push/部署/psql 写/凭证读）。O2 墙钟=会话 10min kill+放行下一条。
  A2 白名单命中计数→`/var/lib/alert-bridge/hits.jsonl`（日命中归零=腐化
  信号，digest 06:10 批消费）。
- `alert-bridge.service` —— systemd 单元（systemd 255 核过：NoNewPrivileges
  +ProtectSystem=strict+ReadWritePaths 收窄写面+Restart=always）。
- `provision.sh` —— 一键配给（本机：unit 安装+env 文件模板+hits 目录；
  SG：psql 只读 role `nox_ro`+ssh 只读 key 注入）。

凭据纪律（设计稿 §10.2 审计发现对位）：ssh 取证=独立只读 key
（command= 前缀限只读令集）；psql=独立只读 role 非应用凭据借。
