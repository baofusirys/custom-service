package config

// Version 项目语义化版本号。
//
// ⚠️ 升级时必须同时改 2 处保持一致：
//   1. 仓库根 /VERSION 文件（集成方 curl raw.githubusercontent 拉这个查 upstream 最新版）
//   2. 本文件 Version 常量（backend embed 后 /api/version 接口对外暴露）
//
// 升级后流程：
//   1. 改这两处 + CHANGELOG.md 加一条
//   2. git commit
//   3. git tag v0.2.x && git push --tags  → GitHub Actions 自动 build :0.2.x GHCR 镜像
//   4. gh release create v0.2.x --notes-from-tag  → 生成 GitHub Release
//
// 后续可优化为 Go embed VERSION 文件自动同步（暂不做避免改 backend Dockerfile context）。
const Version = "0.5.1"
