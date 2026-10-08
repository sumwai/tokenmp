# Changelog

## [0.1.10](https://github.com/sumwai/tokenmp/compare/v0.1.9...v0.1.10) (2026-10-08)


### Features

* **plugin:** 插件改由本机清单与 admin plugin 命令管理 ([#111](https://github.com/sumwai/tokenmp/issues/111)) ([5754895](https://github.com/sumwai/tokenmp/commit/575489554f10c42894a8698c523a9861213a1f15))
* **plugin:** 清单改动运行期生效，装不上的插件只跳过自己 ([#113](https://github.com/sumwai/tokenmp/issues/113)) ([a0cfe8a](https://github.com/sumwai/tokenmp/commit/a0cfe8ac63631982f1348cffbe0c164eae2ac528))
* **plugin:** 离线校验命令与可运行示例，补全流式事件对象字段表 ([#107](https://github.com/sumwai/tokenmp/issues/107)) ([f8cd130](https://github.com/sumwai/tokenmp/commit/f8cd13019c3c7752bd988ada7065f30d3ea7e946))
* 注册事务内开户，角色取值增加 partner ([#108](https://github.com/sumwai/tokenmp/issues/108)) ([f37a816](https://github.com/sumwai/tokenmp/commit/f37a8160c729b2d2738e37bc201b43f5faafce11))
* 用户侧端点契约与商家概念的收敛 ([#112](https://github.com/sumwai/tokenmp/issues/112)) ([617b3a5](https://github.com/sumwai/tokenmp/commit/617b3a58ecea7ad6a128c05829beb1d381bea614))
* 页面账号体系——注册登录、密码生命周期与第三方登录 ([#99](https://github.com/sumwai/tokenmp/issues/99)) ([4c3192e](https://github.com/sumwai/tokenmp/commit/4c3192e84f6d7d61040fc02edc69e19b935b9882))


### Bug Fixes

* **plugin:** SIGHUP 强制重载中间件，换代后重跑作用域漂移检查 ([#106](https://github.com/sumwai/tokenmp/issues/106)) ([65e1bac](https://github.com/sumwai/tokenmp/commit/65e1bac9b4722445ba3d618f72d3ab319d94ab6b))
* **plugin:** 失败日志补 JS 栈与请求标识，装配期拦下非函数钩子 ([#100](https://github.com/sumwai/tokenmp/issues/100)) ([d65f5db](https://github.com/sumwai/tokenmp/commit/d65f5dbaeb76ec8c212d88230ff459c4db821868))
* **plugin:** 热重载单飞与退避，状态读取不再触发重编译 ([#103](https://github.com/sumwai/tokenmp/issues/103)) ([36b272d](https://github.com/sumwai/tokenmp/commit/36b272d3a8ec8475a4c4cfcd73f7cce12048f913))
* **plugin:** 热重载覆盖运行期依赖，补齐强制重载与不可用计数 ([#105](https://github.com/sumwai/tokenmp/issues/105)) ([ed09dfd](https://github.com/sumwai/tokenmp/commit/ed09dfd4dab2bd4209abb78915c4a1d871add1c5))


### Documentation

* **plugin:** 写明混合帧边界，纠正 stale 的可读范围，补取用开销基准 ([#110](https://github.com/sumwai/tokenmp/issues/110)) ([39db7d5](https://github.com/sumwai/tokenmp/commit/39db7d51973cb2195d2f427d4b998fae44912062))

## [0.1.9](https://github.com/sumwai/tokenmp/compare/v0.1.8...v0.1.9) (2026-10-07)


### Bug Fixes

* 措辞扫描改为有界，并补上语义差分、生成式与 fuzz 测试 ([#98](https://github.com/sumwai/tokenmp/issues/98)) ([1a1f6ab](https://github.com/sumwai/tokenmp/commit/1a1f6ab824645c67651617a3d1cde622a2a6b5d8))
* 流式错误帧丢失类别与动作集，并补上模拟上游的逐类测试 ([#95](https://github.com/sumwai/tokenmp/issues/95)) ([5688db0](https://github.com/sumwai/tokenmp/commit/5688db0802d03d5a7b24280677dc8f42910b8751))

## [0.1.8](https://github.com/sumwai/tokenmp/compare/v0.1.7...v0.1.8) (2026-10-07)


### Features

* 中间件作用域——按名字声明范围，越界不进入 JS 运行时 ([#89](https://github.com/sumwai/tokenmp/issues/89)) ([e450ffd](https://github.com/sumwai/tokenmp/commit/e450ffdedb9e3c81a33e1f794b7a74b1957f38a4)), closes [#88](https://github.com/sumwai/tokenmp/issues/88)
* 推理透传——插件流结束钩子与 OpenAI 编码器下发 reasoning_content ([#86](https://github.com/sumwai/tokenmp/issues/86)) ([daad501](https://github.com/sumwai/tokenmp/commit/daad5011a1cbba9133f1e4f49471030037a9807e)), closes [#85](https://github.com/sumwai/tokenmp/issues/85)


### Bug Fixes

* **failure:** 区分「未带分类」与「分类为 other」 ([#94](https://github.com/sumwai/tokenmp/issues/94)) ([1b899b4](https://github.com/sumwai/tokenmp/commit/1b899b48f2e26478618a105b5d54495a269f91cc))
* **plugin:** 拦下 async 钩子并补上被静默吞掉的配置告警 ([#92](https://github.com/sumwai/tokenmp/issues/92)) ([9412bc8](https://github.com/sumwai/tokenmp/commit/9412bc8adc4d657b8aff8dad2c0519fd232322e5))


### Code Refactoring

* 失败分类收敛为单一出处 ([#91](https://github.com/sumwai/tokenmp/issues/91)) ([4079a45](https://github.com/sumwai/tokenmp/commit/4079a4564c0357107db73e5c7b58975d8682fcd7))

## [0.1.7](https://github.com/sumwai/tokenmp/compare/v0.1.6...v0.1.7) (2026-10-06)


### Features

* 上游失败的按类分类与换凭据——额度与余额耗尽不再当成限流 ([#84](https://github.com/sumwai/tokenmp/issues/84)) ([c498525](https://github.com/sumwai/tokenmp/commit/c49852515b15e863121dc73083fd5b64e4e4701e)), closes [#83](https://github.com/sumwai/tokenmp/issues/83)
* 渠道 config 新增 headers 键——客户端优先的静态上游请求头 ([#77](https://github.com/sumwai/tokenmp/issues/77)) ([8c7ec6b](https://github.com/sumwai/tokenmp/commit/8c7ec6baece7186f53ad6efcfac1d97cca02052b)), closes [#76](https://github.com/sumwai/tokenmp/issues/76)


### Bug Fixes

* 管理面两处缺口——credential 缺 enable、channel list 读 NULL config 崩溃 ([#82](https://github.com/sumwai/tokenmp/issues/82)) ([9e44f6e](https://github.com/sumwai/tokenmp/commit/9e44f6ea7d7d1a225d645c4e1cbf87bff853e361)), closes [#81](https://github.com/sumwai/tokenmp/issues/81)


### Documentation

* README 补部署与运行入口，归档带上 docs 与 RELEASE ([#79](https://github.com/sumwai/tokenmp/issues/79)) ([6dccb82](https://github.com/sumwai/tokenmp/commit/6dccb823b89c243a42c9f699d9c7ef5d2589f95c)), closes [#78](https://github.com/sumwai/tokenmp/issues/78)

## [0.1.6](https://github.com/sumwai/tokenmp/compare/v0.1.5...v0.1.6) (2026-10-06)


### Features

* Gemini 协议方言 ([#69](https://github.com/sumwai/tokenmp/issues/69)) ([ce3d89f](https://github.com/sumwai/tokenmp/commit/ce3d89f0aaba6a9758686f6b1cf5d9935c260cf6))
* 上游信标头——登录态的精准标记 ([#67](https://github.com/sumwai/tokenmp/issues/67)) ([bae29e1](https://github.com/sumwai/tokenmp/commit/bae29e12c20c7c56ed10b3c112a005c7f81c6f69))
* 上游套餐与配额——建模、采集与路由消费 ([#70](https://github.com/sumwai/tokenmp/issues/70)) ([8d6b851](https://github.com/sumwai/tokenmp/commit/8d6b851e02e6bdb569ad67053d0b4870f475af4e))
* 用户自助查询端点——余额、包存量与限额窗口 ([#66](https://github.com/sumwai/tokenmp/issues/66)) ([a6ed4d5](https://github.com/sumwai/tokenmp/commit/a6ed4d523a529127dddff6cdd83ddc6a95f60097))
* 网关中间件插件层——moejs 沙箱与逐事件钩子 ([#72](https://github.com/sumwai/tokenmp/issues/72)) ([d3557e6](https://github.com/sumwai/tokenmp/commit/d3557e6c9bf850c0b6d9aa89132df8c50c46035b))
* 订阅型上游的 OAuth 登录与凭据续期 ([#71](https://github.com/sumwai/tokenmp/issues/71)) ([31dc55e](https://github.com/sumwai/tokenmp/commit/31dc55e7f6cdafa3c318b4363687efb5aa520180))


### Documentation

* 同步并行批次落地后的 admin 组、环境变量与运营步骤 ([#75](https://github.com/sumwai/tokenmp/issues/75)) ([6aaa1bb](https://github.com/sumwai/tokenmp/commit/6aaa1bbd698ddc777783360351843c03a8514819)), closes [#73](https://github.com/sumwai/tokenmp/issues/73)

## [0.1.5](https://github.com/sumwai/tokenmp/compare/v0.1.4...v0.1.5) (2026-10-05)


### Features

* admin 管理面子命令集 ([#34](https://github.com/sumwai/tokenmp/issues/34)) ([d2fac98](https://github.com/sumwai/tokenmp/commit/d2fac98b1d719063ca697f7db1ed7a30b2fed62c))
* OpenAPI 规范与协议兼容性说明 ([#56](https://github.com/sumwai/tokenmp/issues/56)) ([999d3fa](https://github.com/sumwai/tokenmp/commit/999d3faf1525b448c1f10ada1d82eec4519d7f7c))
* 上游渠道限流执行——QPS 与并发上限 ([#39](https://github.com/sumwai/tokenmp/issues/39)) ([8d51658](https://github.com/sumwai/tokenmp/commit/8d516582b94a1eb084dae5c882653901a2377501))
* 上游熔断接线——连续失败隔离与半开探测 ([#45](https://github.com/sumwai/tokenmp/issues/45)) ([18af94a](https://github.com/sumwai/tokenmp/commit/18af94a3563d6b0a7e85a793b3ae4715b21f78a8))
* 端到端运营剧本——从入驻到对账的可重复验证 ([#53](https://github.com/sumwai/tokenmp/issues/53)) ([1d03216](https://github.com/sumwai/tokenmp/commit/1d03216124cbeeeebfedd63c12dbbdaa6819eada))
* 结算执行——定价解析、倍率链与账本扣减 ([#30](https://github.com/sumwai/tokenmp/issues/30)) ([c0319b6](https://github.com/sumwai/tokenmp/commit/c0319b639d9a1ec57f923b133b0430d19afe95f3))
* 账户限额执行——窗口聚合判定与管理动作 ([#38](https://github.com/sumwai/tokenmp/issues/38)) ([a597fc3](https://github.com/sumwai/tokenmp/commit/a597fc320060162f6eccf7077021c1a3d32137b1))
* 跨协议路由降级——同协议缺位时的重建转发 ([#43](https://github.com/sumwai/tokenmp/issues/43)) ([387fcaf](https://github.com/sumwai/tokenmp/commit/387fcaffebb8559c01673326f31173bd6e23bda1))
* 限额判定扩展到 api_key 维度 ([#52](https://github.com/sumwai/tokenmp/issues/52)) ([1467650](https://github.com/sumwai/tokenmp/commit/1467650ec685d36aff928edb4e055312a6861c1e))
* 非货币包的跨单位折算率与 fallback 转换 ([#35](https://github.com/sumwai/tokenmp/issues/35)) ([b6423df](https://github.com/sumwai/tokenmp/commit/b6423dffc3ea5dc6b6c3604895277671d6d1f71c))


### Bug Fixes

* request 次数限额永不触发——usage 未写入 request 分量 ([#55](https://github.com/sumwai/tokenmp/issues/55)) ([0724121](https://github.com/sumwai/tokenmp/commit/0724121336bde22a6c59c9083e8578b50d0e66b0))
* 跨协议降级在尝试预算耗尽前不可达 ([#47](https://github.com/sumwai/tokenmp/issues/47)) ([c24dc8c](https://github.com/sumwai/tokenmp/commit/c24dc8c609013453432faa162b964d604bd7ab70))


### Code Refactoring

* 过程式代码下沉——cmd 只剩解析、装配与输出 ([#59](https://github.com/sumwai/tokenmp/issues/59)) ([e070bed](https://github.com/sumwai/tokenmp/commit/e070bed7498d724d4376c87f5fc06be16d42b138))


### Documentation

* 使用与部署文档——运营剧本与环境变量手册 ([#57](https://github.com/sumwai/tokenmp/issues/57)) ([ea43154](https://github.com/sumwai/tokenmp/commit/ea431541fc878fece2c2ad60bd0ee534ca474c90))

## [0.1.4](https://github.com/sumwai/tokenmp/compare/v0.1.3...v0.1.4) (2026-10-05)


### Features

* **credential:** 上游凭据轮换与失败切换 ([#25](https://github.com/sumwai/tokenmp/issues/25)) ([7b401be](https://github.com/sumwai/tokenmp/commit/7b401beb54de8ab97e860dfcf88e4d7ee599c311))
* 尝试级观测接入生产装配 ([#28](https://github.com/sumwai/tokenmp/issues/28)) ([6ced34d](https://github.com/sumwai/tokenmp/commit/6ced34d9e4590869eb59408d4b0a7478e879908f))
* 用量分档承载与 usage 帧抑制 ([#27](https://github.com/sumwai/tokenmp/issues/27)) ([8a643e6](https://github.com/sumwai/tokenmp/commit/8a643e60f19c96ef564bed4c60fa0fffbd7053c9))

## [0.1.3](https://github.com/sumwai/tokenmp/compare/v0.1.2...v0.1.3) (2026-10-05)


### Features

* 核心转发层——serve 子命令与三协议转发 ([#19](https://github.com/sumwai/tokenmp/issues/19)) ([793c147](https://github.com/sumwai/tokenmp/commit/793c147dd9b584d95c2b9e0922cd3c81357a9d64)), closes [#18](https://github.com/sumwai/tokenmp/issues/18)

## [0.1.2](https://github.com/sumwai/tokenmp/compare/v0.1.1...v0.1.2) (2026-10-05)


### Features

* 新增 MySQL 存储层与初版 schema ([#15](https://github.com/sumwai/tokenmp/issues/15)) ([c23c830](https://github.com/sumwai/tokenmp/commit/c23c83072528e2b9643524db2ecb1a87fb991679))
* 新增计费 schema、枚举与 store 读写 ([#17](https://github.com/sumwai/tokenmp/issues/17)) ([351c398](https://github.com/sumwai/tokenmp/commit/351c398d978733f0a3c13a47f754083b01141049))

## [0.1.1](https://github.com/sumwai/tokenmp/compare/v0.1.0...v0.1.1) (2026-10-05)


### Bug Fixes

* 产物版本号与本地构建口径统一为带 v ([#12](https://github.com/sumwai/tokenmp/issues/12)) ([c6f6da3](https://github.com/sumwai/tokenmp/commit/c6f6da37f88e18babe3c62a9220c46ac9225a271))


### Documentation

* changelog 归属写入文档规则 ([#11](https://github.com/sumwai/tokenmp/issues/11)) ([95a7ee6](https://github.com/sumwai/tokenmp/commit/95a7ee64b481355e9393c0d68c1774eb698e5fec))
* README 恢复 changelog 链接 ([#9](https://github.com/sumwai/tokenmp/issues/9)) ([7296ac3](https://github.com/sumwai/tokenmp/commit/7296ac31aae5b69241fffecd73486b0be2979077))

## 0.1.0 (2026-10-05)


### Features

* Issue 模板、PR 模板与 PR 标题校验 ([c7f4da1](https://github.com/sumwai/tokenmp/commit/c7f4da169a76c175d43e4462f1af002f15f7acb7))
* 单二进制骨架、本地门禁与发布链路 ([947625e](https://github.com/sumwai/tokenmp/commit/947625e7d4ddeb8aa141ae420ed84564d1b72121))


### Bug Fixes

* changelog 仅保留主标题，完全交由 release-please 维护 ([#5](https://github.com/sumwai/tokenmp/issues/5)) ([c627ed6](https://github.com/sumwai/tokenmp/commit/c627ed6aa53d9a148d67ca280113d817011f738e))
* 首发版本定为 0.1.0 并修正 changelog 头部 ([#3](https://github.com/sumwai/tokenmp/issues/3)) ([24a8678](https://github.com/sumwai/tokenmp/commit/24a86783db088f0e68352bbd446da35e18770e02))


### Documentation

* 改动一律走分支经 PR 合并 ([#2](https://github.com/sumwai/tokenmp/issues/2)) ([d77aed9](https://github.com/sumwai/tokenmp/commit/d77aed9b7ba82075ba20afd82fb40b5bc9b80801))
* 移除本机环境信息并固化该禁令 ([858b7f0](https://github.com/sumwai/tokenmp/commit/858b7f00cfd13cbfc331ad3bb839abc5e59a7d8c))
* 补充 gh CLI 驱动的 Issue 与 PR 协作流程 ([dd42732](https://github.com/sumwai/tokenmp/commit/dd427326c92120f9fc36e84b41e67a7038dcf590))
* 路径占位写法并入信息规范条目 ([99d00dc](https://github.com/sumwai/tokenmp/commit/99d00dc9fa8885cf8ea470e74d9edbd72c0871c2))
