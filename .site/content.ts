// 站点全部文案集中在这里：改文案只改本文件。
// 结构上 zh / en 必须保持同样的键，页面组件按 lang 取用。

export type Lang = 'zh' | 'en';

export const SITE = {
  url: 'https://golder-cli.pages.dev',
  repo: 'https://github.com/getan/golder',
  releases: 'https://github.com/getan/golder/releases',
  issues: 'https://github.com/getan/golder/issues',
  changelog: 'https://github.com/getan/golder/blob/master/CHANGELOG.md',
  siteRepo: 'https://github.com/getan/golder-site',
  // install.sh 由 CI 从主仓库镜像到本站（同一份源码，脚本自带 sha256 校验）；
  // 站点域名对大陆网络更友好，raw.githubusercontent.com 时常不可达。
  installCmd: 'curl -fsSL https://golder-cli.pages.dev/install.sh | sh',
};

export interface Feature {
  icon: string;
  title: string;
  body: string;
}

export interface Step {
  title: string;
  body: string;
  codes: string[];
}

export interface FaqItem {
  q: string;
  a: string; // 允许内联 HTML（链接）
}

const zh = {
  meta: {
    title: 'golder — 终端里的 AI 编码助手',
    description:
      '用 Go 编写的终端 AI 编码助手：读写文件、执行命令、检索代码、抓取网页，在对话中完成从理解需求到改好代码的闭环。单二进制，MIT 开源。',
  },
  nav: {
    demo: '演示',
    features: '特性',
    quickstart: '上手',
    faq: 'FAQ',
    github: 'GitHub',
    switch: 'English',
  },
  hero: {
    eyebrow: 'golder-cli.pages.dev · Go · MIT',
    title: '终端里的 AI 编码助手',
    lead:
      'golder 用 Go 写成一个单二进制：读写文件、执行命令、检索代码、抓取网页，把「理解需求 → 改好代码」跑成闭环。默认进入全屏 TUI，也能无头跑进脚本和 CI。',
    ctaPrimary: '三步上手',
    ctaSecondary: '在 GitHub 上看源码',
    installHint: 'macOS / Linux 一键安装 · Windows 从 Releases 下载 zip · 不需要任何运行时',
    videoTitle: 'golder — 真实会话（空闲段已掐掉）',
    videoCaption:
      '真实录制，无剪辑造假：go test 红 → 模型自己读码、打补丁、复跑 → 全绿收尾（26 秒）',
  },
  features: {
    title: '不是又一个壳',
    sub: '安全边界、长任务、会话管理都是内建的——这些才是每天用得到的部分。',
    items: [
      {
        icon: 'terminal',
        title: '单二进制，三种形态',
        body:
          '全屏 TUI、行式 REPL、无头 -p 一套命令完全对齐；--output-format stream-json 逐行 JSON 事件，方便脚本和 CI 消费。',
      },
      {
        icon: 'shield',
        title: '审批分级，不必事事都问模型',
        body:
          '四档权限：只读 / 每次询问 / 自动 / 完全放行。auto 档先过本地规则表——只读与日常操作按档位直接处理，不花模型往返；危险与未知的调用才交给模型评审，并给出一行中文理由。',
      },
      {
        icon: 'box',
        title: '操作系统级沙箱',
        body:
          'macOS 用 sandbox-exec、Linux 用 bubblewrap：读白名单（$HOME 里的密钥、凭据、历史一律读不到）、写限项目与临时目录、网络默认关闭。',
      },
      {
        icon: 'gauge',
        title: '长任务不掉线',
        body:
          '/compact 压缩、上下文预算工具、/goal 自主目标循环、子 Agent 派发、持久记忆与 /dream 整理——跑几小时的大活有配套的兜底。',
      },
      {
        icon: 'branch',
        title: '会话是资产',
        body:
          '/resume 历史会话、/fork 从任意消息分叉、/tree 浏览分支树、/rewind 把文件与对话一起回滚、/export 往返存档。',
      },
      {
        icon: 'shield',
        title: '读取越界要先问过你',
        body:
          '读取类工具默认只读工作区；越界会先弹窗问——只读这一次、本会话允许该目录，或记住（写进授权表，/boundary 可查）。没人可问时（无头、子 Agent）失败关闭。完全放行关掉的是审查与沙箱，不是知情同意。',
      },
      {
        icon: 'terminal',
        title: '跑在 tuios / herdr 里就是一个 agent',
        body:
          '在 tuios 窗口或 herdr pane 里启动，golder 自己上报状态：rail 上出现工作 / 等你 / 完成，审批与提问进 Inbox，daemon 重启后带着 golder --resume 回到原会话。两个宿主都不用改，tuios 只需装一份 harness manifest。',
      },
      {
        icon: 'globe',
        title: '网络现实友好',
        body:
          '19 个内置网关（OpenAI / Anthropic / DeepSeek / OpenRouter / Google…），也可指向任意 OpenAI 兼容端点。/proxy 按 provider 单独走代理，未选中的直连、不继承 shell 的 HTTP_PROXY。',
      },
    ] as Feature[],
  },
  quickstart: {
    title: '三步上手',
    sub: '二进制安装不需要 Go；下面三条命令在 macOS / Linux 上可直接复制。',
    steps: [
      {
        title: '安装',
        body: '一条命令装好，自动识别系统与架构；Windows 从 Releases 下载 .zip 解压即可。',
        codes: [SITE.installCmd],
      },
      {
        title: '配置凭证',
        body: '默认网关 OpenCode 只需一个 API Key；也可指向任意 OpenAI 兼容端点，或用本地 Ollama。',
        codes: ['export OPENCODE_API_KEY=...', 'golder   # 直接开聊'],
      },
      {
        title: '开始用',
        body: '默认进入全屏 TUI；加 -p 是无头模式，适合脚本与 CI。',
        codes: ['golder', 'golder -p "读取 README 并总结这个仓库"'],
      },
    ] as Step[],
  },
  faq: {
    title: '常见问题',
    sub: '还有别的问题？到 GitHub 提 issue。',
    items: [
      {
        q: '需要 Node 或别的运行时吗？',
        a: '不需要。golder 是 Go 编译的单个二进制，下载即用；只有从源码构建才需要 Go 1.27+。',
      },
      {
        q: '我的代码会被上传到哪里？',
        a: '只会发送给你自己选择的模型网关（OpenAI、Anthropic、DeepSeek 或你指定的任意端点）。会话、记忆、凭据都存在本地 ~/.golder，golder 没有自己的后端。',
      },
      {
        q: '支持哪些模型？',
        a: '内置 19 个网关（含审批用的决策模型 typesafe，它不接会话），可用 /provider 和 /model 在会话内切换，推理档位从 off 到 max；也可以指向本地 Ollama。',
      },
      {
        q: '安全吗？它会乱改我的文件吗？',
        a: '默认 auto 档：只读与日常操作由本地规则表按档位直接处理，危险与未知的调用交给模型评审——低风险放行、可隔离的进沙箱、高危拒绝，并给出一行理由。想先观察可以切到 read-only。',
      },
      {
        q: '国内网络能用吗？',
        a: 'golder 支持按 provider 配代理，国内模型直连；注意本站是 Cloudflare 免费子域，少数网络下可能打不开，仓库和 Releases 始终是备选入口。',
      },
      {
        q: '和 pi 是什么关系？',
        a: 'golder 是 pi 的 Go 重实现，此后独立演进，MIT 许可。',
      },
      {
        q: '怎么更新？',
        a: '跑 golder update 即自更新；重跑安装脚本也可以。',
      },
    ] as FaqItem[],
  },
  footer: {
    license: 'MIT License',
    siteSource: '本站源码',
    mainRepo: '主仓库',
    issues: '问题反馈',
    releases: '版本发布',
  },
  notFound: {
    title: '404 — 页面不存在',
    body: '这个地址没有内容。回首页，或者去 GitHub 看源码。',
    home: '回首页',
  },
};

const en = {
  meta: {
    title: 'golder — the AI coding agent in your terminal',
    description:
      'A terminal AI coding agent written in Go: read and write files, run commands, search code, fetch the web — closing the loop from understanding a task to shipping the change. Single binary, MIT.',
  },
  nav: {
    demo: 'Demo',
    features: 'Features',
    quickstart: 'Quickstart',
    faq: 'FAQ',
    github: 'GitHub',
    switch: '简体中文',
  },
  hero: {
    eyebrow: 'golder-cli.pages.dev · Go · MIT',
    title: 'The AI coding agent in your terminal',
    lead:
      'golder is a single Go binary: read and write files, run commands, search code, fetch the web — closing the loop from “understand the task” to “the change is in”. Full-screen TUI by default, headless for scripts and CI.',
    ctaPrimary: 'Get started',
    ctaSecondary: 'Source on GitHub',
    installHint: 'One-line install for macOS / Linux · Windows: download the zip from Releases · no runtime required',
    videoTitle: 'golder — a real session (idle time trimmed)',
    videoCaption:
      'Recorded for real, nothing staged: red tests → the model reads, patches, re-runs → all green (26s)',
  },
  features: {
    title: 'Not another wrapper',
    sub: 'Safety boundaries, long tasks, and session management are built in — the parts you actually use every day.',
    items: [
      {
        icon: 'terminal',
        title: 'One binary, three shapes',
        body:
          'Full-screen TUI, line REPL, and headless -p with identical commands; --output-format stream-json emits one JSON event per line for scripts and CI.',
      },
      {
        icon: 'shield',
        title: 'Graded approvals — a model is asked only when it matters',
        body:
          'Four permission modes: read-only / ask / auto / full-access. In auto, a local rule table settles reads and ordinary local work by mode — no model round trip — and only dangerous or unknown calls go to a model review, with a one-line rationale.',
      },
      {
        icon: 'box',
        title: 'OS-level sandboxing',
        body:
          'sandbox-exec on macOS, bubblewrap on Linux: read allow-list (secrets, credentials and history under $HOME are unreadable), writes limited to the project and temp dirs, network off by default.',
      },
      {
        icon: 'gauge',
        title: 'Built for long tasks',
        body:
          '/compact summarization, context-budget tools, autonomous /goal loops, sub-agent dispatch, persistent memory and /dream consolidation.',
      },
      {
        icon: 'branch',
        title: 'Sessions are assets',
        body:
          '/resume past sessions, /fork from any message, /tree to browse branches, /rewind to roll back files and conversation together, /export for round-trip archives.',
      },
      {
        icon: 'shield',
        title: 'Reads outside the workspace ask first',
        body:
          'The read tools stay inside the workspace until you say otherwise — the dialog offers just this read, this session for that directory, or remembered (written to the authorization table, shown by /boundary). With nobody to ask (headless, a sub-agent) it fails closed. Full-access turns off review, not consent.',
      },
      {
        icon: 'terminal',
        title: 'A first-class agent in tuios / herdr',
        body:
          'Start it in a tuios window or a herdr pane and golder reports its own state: it shows up on the rail (working / waiting on you / done), approvals land in the Inbox, and a daemon restart brings the conversation back with golder --resume. Neither host changes; tuios needs one harness manifest.',
      },
      {
        icon: 'globe',
        title: 'Real-world networking',
        body:
          '19 built-in gateways (OpenAI / Anthropic / DeepSeek / OpenRouter / Google…) plus any OpenAI-compatible endpoint. /proxy routes per provider: unselected ones go direct and ignore the shell’s HTTP_PROXY.',
      },
    ] as Feature[],
  },
  quickstart: {
    title: 'Get started in three steps',
    sub: 'No Go toolchain needed for the binary install; the commands below work on macOS / Linux.',
    steps: [
      {
        title: 'Install',
        body: 'One command detects OS and architecture. On Windows, download the .zip from Releases.',
        codes: [SITE.installCmd],
      },
      {
        title: 'Configure a credential',
        body: 'The default gateway (OpenCode) needs a single API key; any OpenAI-compatible endpoint or a local Ollama works too.',
        codes: ['export OPENCODE_API_KEY=...', 'golder   # start chatting'],
      },
      {
        title: 'Run it',
        body: 'No flags: full-screen TUI. Add -p for headless mode — good for scripts and CI.',
        codes: ['golder', 'golder -p "read the README and summarize this repo"'],
      },
    ] as Step[],
  },
  faq: {
    title: 'FAQ',
    sub: 'Something else on your mind? Open an issue on GitHub.',
    items: [
      {
        q: 'Does it need Node or any runtime?',
        a: 'No. golder is a single Go binary — download and run. Only building from source needs Go 1.27+.',
      },
      {
        q: 'Where does my code go?',
        a: 'Only to the model gateway you choose (OpenAI, Anthropic, DeepSeek, or any endpoint you point it at). Sessions, memory and credentials live locally in ~/.golder; golder has no backend of its own.',
      },
      {
        q: 'Which models are supported?',
        a: '19 built-in gateways (including the typesafe decision model, which does not serve conversations), switchable at runtime with /provider and /model, reasoning levels from off to max — or point it at a local Ollama.',
      },
      {
        q: 'Is it safe? Will it edit my files behind my back?',
        a: 'The default auto mode settles reads and ordinary local work locally by mode, and sends dangerous or unknown calls to a model review: low risk runs, isolatable calls go into the sandbox, dangerous ones are denied — each with a one-line rationale. Switch to read-only to just watch.',
      },
      {
        q: 'Does the site work from mainland China?',
        a: 'golder itself supports per-provider proxies and connects directly to domestic models. This site, however, is a free Cloudflare subdomain and may be unreachable on some networks — the GitHub repo and Releases are always the fallback.',
      },
      {
        q: 'Relation to pi?',
        a: 'golder is a Go re-implementation of pi, evolving independently under MIT.',
      },
      {
        q: 'How do I update?',
        a: 'Run golder update (self-update), or re-run the install script.',
      },
    ] as FaqItem[],
  },
  footer: {
    license: 'MIT License',
    siteSource: 'Site source',
    mainRepo: 'Main repo',
    issues: 'Issues',
    releases: 'Releases',
  },
  notFound: {
    title: '404 — Page not found',
    body: 'Nothing lives at this address. Head back home, or read the source on GitHub.',
    home: 'Back home',
  },
};

export const content = { zh, en };

export function langPath(lang: Lang, path = '/'): string {
  return lang === 'en' ? `/en${path === '/' ? '/' : path}` : path;
}

export function otherLang(lang: Lang): Lang {
  return lang === 'en' ? 'zh' : 'en';
}

/** 从 FAQ 答案里剥掉标签，供 JSON-LD 使用。 */
export function stripTags(html: string): string {
  return html.replace(/<[^>]+>/g, '');
}
