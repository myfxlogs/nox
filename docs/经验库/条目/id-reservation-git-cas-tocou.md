---
id: id-reservation-git-cas-tocou
title: 顺序号撞号是 TOCTOU 结构竞态，占号先行+git push CAS 根治
type: pattern
tags: [多席并发, D号/工单号, TOCTOU, git, 占号制, 门禁]
date: 2026-09-30
---

多席共享顺序号（D# 工单号）的撞号，在「双查干净→写文档 20-60min→commit」流程下是结构竞态非纪律缺失（同日三案 D-590/D-594/D-596 全部双查干净仍撞；史案 D-127/D-287）。根治=**占号先行**：decisions 登记簿尾加占位行→单 commit 立即 push——git 单 ref 推送天然 CAS（先推者得号，被拒者秒级取下一号）；正式文档后写零窗口；弃票标 abandoned 号不复用；引用既有号不触发。

pre-push 门禁兜底的豁免语义（图形态不可分结论）：「推送分支不含 claim 的撞号=拒（主窗，即并行工作 TOCTOU 形态）」；「claim 在推送 ancestry 内=放行」——因合法收编流（claim 经 seat 分支并入）与「已同步后仍撞号」在 git 图上不可分，强行收紧毒化收编流；该残余面归双查纪律域（占号行在推送树登记簿肉眼可见）。选型否决：号段分区（破共享递增序）/PG sequence（文档流程引运行库依赖）/纯门禁（治标）。

证据：arb 2026-09-30 三案+门禁生产实弹拦截（落后型撞号拒推 FAIL 实证）；D-596/D-598。
