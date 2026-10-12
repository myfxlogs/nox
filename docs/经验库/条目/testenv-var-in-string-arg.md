---
id: testenv-var-in-string-arg
title: 测试环境变量清单禁只扫 os.Getenv 字面量——字符串参数式注入必漏
type: pitfall
tags: [测试DSN, 环境变量, TestDSN, 假红, 误判回归]
date: 2026-09-30
---

清点测试所需环境变量时只 grep `os.Getenv("...")` 字面量，会漏掉**以字符串参数注入**的封装（如 `testutil.TestDSN(t, "ARB_EXEC_TEST_PG_DSN")`）——变量名藏在函数参数里。后果链：DSN 缺供→D-207 fail-closed 测试批量假红（80 处）→误判「master 批量回归」→险些错怪并行席位。防法：清单 grep 双模式 `os.Getenv\("` + 封装调用 `TestDSN\(\w+, "`（凡 env 封装一律入清单）；假红批量出现先查「是否缺供」再查「是否回归」。

证据：arb 2026-09-30 D-591 复审（漏 ARB_TEST_PG_SERVICE_DSN/ARB_EXEC_TEST_PG_DSN/ARB_RECONCILE_TEST_PG_DSN 三个，两轮重跑）。
