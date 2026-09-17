# 参与 Ferry 开发

[English](CONTRIBUTING.md) · **中文**

Ferry 按阶段推进。每个阶段先冻结契约，再照着契约并行跑若干条任务。完整的构建
计划在 `docs/dev/` 下 —— 那是有意只写中文的开发派工文档，不属于双语文档集。

## 基本规矩

- 一个阶段冻结的契约，并行任务不能改。契约里找不到你要的东西，停下来报告，
  不要自己发明。
- 每个文件只有一个 owner。白名单以外的文件一律只读。
- 不要自己加依赖。`go.mod` 只由开启该阶段的那条任务修改。

## 代码约定

- 可能阻塞的函数，第一个参数是 `context.Context`。
- 时间一律经 `internal/clock` 注入。测试用假时钟推进，不要 sleep 等业务超时。
- 错误用 `internal/fsp` 里的哨兵错误，外层用 `fmt.Errorf("...: %w", err)` 包装。
  占位代码之外不要 panic。
- 日志用 `log/slog`。不要记录 token、配对码和消息正文；正文只记长度。
- 所有面向用户的文字都要经 i18n。键放在 `locales/en.toml` 与
  `locales/zh-CN.toml`；代码里不写死中英文。
- 测试替身放 `internal/testkit/**` 或本包的 `_test.go` 里。宁可重复，
  也不新增公共 utils 包。
- 标识符、注释和提交信息用英文，面向用户的文档中英双语。

## 双语同步义务

这条不是可选项，而且有工具强制。

- `README.md`、`CONTRIBUTING.md`、`SECURITY.md`、`spec/fsp.md` 各有一份
  `.zh-CN.md` 对应文件。改一边就在同一个提交里改另一边。
- `docs/en/` 与 `docs/zh-CN/` 下的相对路径集合完全相同。
- 成对的两份文件，`## ` 与 `### ` 标题数量、围栏代码块数量都必须相等，
  去掉注释后代码块逐行相同。注释可以翻译，命令本身不能改。
- 英文文件在前五行内链到中文版，中文文件在前五行内链回英文版。
- `locales/en.toml` 与 `locales/zh-CN.toml` 的键集合完全相同。
- 中文文件可以在第一行用 `<!-- source_commit: abc1234 -->` 钉住它翻译自的
  英文版本。

## 提交前自查

```sh
go build ./...
go vet ./...
test -z "$(gofmt -l .)"
go test ./...
go run ./scripts/check-i18n
```

## 提交信息

一行，英文，格式为 `pN-tK: what changed`，例如
`p1-t2: acp driver and registry`。贡献者不推标签。
