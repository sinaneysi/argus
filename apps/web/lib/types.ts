// Shapes of the Go JSON API (apps/api/internal/app/api*.go).

export type Banner = { text: string; kind: "ok" | "warn" | "fail" | "info"; refresh?: number; segs?: { text: string; href?: string }[] };

export type Me = {
  username: string;
  role: "admin" | "viewer" | string;
  source: string;
  mustChangePassword: boolean;
  mfa: { enabled: boolean; via: string; at: number; enrolRequired: boolean };
  permissions: Record<string, boolean>;
  theme: "auto" | "light" | "dark";
  demo: boolean;
  demoBanner?: string;
  product: string;
  subtitle: string;
};

export type NavLink = { label: string; href: string; count?: number; countTitle?: string; alarm?: boolean };
export type NavGroup = { key: string; name: string; icon: string; question: string; links: NavLink[] };
export type Nav = {
  groups: NavGroup[];
  footer: NavLink[];
  search: { action: string; param: string; hidden: Record<string, string>; placeholder: string };
};

export type Status = {
  now: number;
  dataAsOf: number;
  checksAt: number;
  state: "ok" | "stale" | "failed" | "unknown";
  failed: string[];
  failedHref: string;
  tz: string;
};

export type AuthOptions = {
  product: string;
  subtitle: string;
  sso: boolean;
  passwordLogin: boolean;
  demo: boolean;
  demoBanner: string;
  demoUsers?: { username: string; label: string }[];
  demoPassword?: string;
};

export type Link = { text: string; href?: string };

export type TodayItem = {
  id: number;
  n?: number;
  title: string;
  href: string;
  linkLabel: string;
  doThis: string;
  whyNow: string;
  estMin: number;
  estBasis: string;
  status: "planned" | "doing" | "done" | string;
  slot: string;
  area: string;
  env: string;
  envShort?: string;
  needsWindow: boolean;
  needsApproval: boolean;
  carried: number;
  startedAt?: number;
  ref: string;
  score: number;
  urgency: number;
  impact: number;
  confidence: number;
  details: Link[];
  promptUrl?: string;
  laterReason?: string;
  closeHref?: string;
  closeLabel?: string;
  autoDone: boolean;
  note?: string;
  actualMin?: number;
};

export type ReviewRow = {
  sel: string;
  kind: string;
  kindRow: string;
  id: string;
  title: string;
  severity?: string;
  sevKey: "critical" | "high" | "medium" | "none" | "low";
  env?: string;
  envLabel?: string;
  prod: boolean;
  targets: string[];
  source?: string;
  sourceLabel?: string;
  at: number;
  waiting: string;
  due?: number;
  dueWord?: string;
  dueLeft?: string;
  dueSoon?: boolean;
  summary?: string;
};

export type ReviewSummary = {
  total: number;
  estMin: number;
  kinds: { key: string; label: string; row: string; count: number }[];
  top: ReviewRow[];
};

export type Today = {
  on: boolean;
  canAct?: boolean;
  admin?: boolean;
  review: ReviewSummary;
  waiting?: { group: string; title: string; href: string; count: number; detail: string }[];
  planAt?: string;
  plan: null | {
    id: number;
    day: string;
    dayLabel: string;
    isToday: boolean;
    budgetMin: number;
    maxItems: number;
    maxRisky: number;
    plannedMin: number;
    doneMin: number;
    actualMin: number;
    updatedAt: number;
  };
  open?: TodayItem[];
  done?: TodayItem[];
  later?: TodayItem[];
  banner?: Banner;
  incidents?: TodayIncidents;
  checks?: TodayChecks;
};

export type Fact = { label: string; value: string; href?: string; mono?: boolean; missing?: boolean };
export type Section = {
  text?: string;
  items?: Link[];
  facts?: Fact[];
  code?: string;
  codeLabel?: string;
  missing?: boolean;
  noField?: boolean;
};
export type Action = {
  action?: string;
  href?: string;
  label: string;
  style: "primary" | "secondary" | "danger";
  reason?: string;
  reasonRequired?: boolean;
  reasonMin?: number;
  disabled?: string;
  confirm?: boolean;
};

export type Plan = {
  version: number;
  steps: { text: string; mode: string; targets: string[] }[];
  writes: number;
  estimate: string;
  spentUsd: number;
  limits: string[];
  path: string;
  pathLabel: string;
  questions: { text: string; blocking: boolean }[];
  taskHref: string;
  blocked: string;
};

export type ReviewDetail = {
  row: ReviewRow;
  facts: Fact[];
  outcome: Section;
  evidence: Section;
  after: Section;
  afterTitle: string;
  more: Fact[];
  actions: Action[];
  note: string;
  readOnly: boolean;
  step: string;
  plan?: Plan;
};

export type Option = { value: string; label: string };

export type Review = {
  admin: boolean;
  total: number;
  estMin: number;
  kinds: { key: string; label: string; row: string; verb: string; count: number }[];
  kindTotal: number;
  query: { kind: string; q: string; sev: string; env: string; src: string; sort: string; page: number; sel: string; step: string };
  filtered: boolean;
  matched: number;
  page: number;
  pages: number;
  pageSize: number;
  capped: boolean;
  perKind: number;
  options: { sev: Option[]; env: Option[]; src: Option[]; sort: Option[] };
  order: string;
  items: ReviewRow[];
  fullMode: boolean;
  banners: Banner[];
  refresh: boolean;
  selected: ReviewDetail | null;
  selGone: boolean;
};

// The Incidents section (apps/api/internal/app: api_shared.go, api_incidents.go, api_rca.go).
export type Seg = { text: string; href?: string };
export type FacetOpt = Option & { count: number };
export type Facet = { key: string; label: string; value: string; any: boolean; options: FacetOpt[] };
export type Tile = { label: string; figure: string; sub: string; href?: string; current?: boolean; hot?: boolean };
export type ListState = {
  sortKey?: string;
  noSearch?: boolean;
  path: string; q: string; sort: string; sorts: Option[]; filtered: boolean; clearHref: string; matched: number; total: number; noun: string;
  page: number; pages: number; from: number; to: number; hidden: Record<string, string>; prev?: string; next?: string;
};
export type Lazy = { rows: Option[]; status: string; statusKind: string; source: string };
export type MdDoc = { md: string; foldOptions?: string; foldPrompt?: string };
export type FixOption = { n: number; title: string; kind?: string; recommended: boolean; kv: Option[]; what: string; approval?: string; approvalTitle?: string };
export type Fix = {
  options: FixOption[]; note: Seg[]; prompt: string; promptHref?: string; checks: string[]; prod: boolean; releaseNote?: string;
  action: null | { heading: string; body: Seg[]; button?: Seg };
};
export type TimelineEv = { at: number; kind: string; source: Seg[]; title?: string; detail: Seg[] };
export type ProbItem = { type: string; label: string; href?: string; title?: string; status?: string; env?: string; exists: boolean };
export type ProbSuggestion = {
  id: number; confidence: string; linkWord: string; from: string; at: number; reason: string; canDecide: boolean;
  item?: ProbItem; problem?: { id: number; label: string; title: string };
};
export type ProblemPanel = {
  banners: Banner[]; banner: Seg[] | null; back: string; ref: string; suggestions: ProbSuggestion[];
  link: null | { summary: string; pq: string; picks: { id: number; title: string; status: string }[]; types: Option[]; button: string; keep: Record<string, string>; path: string };
  create: null | { title: string; noun: string; from: string; first: boolean };
};
export type IncidentRow = {
  id: number; title: string; subject: string; family: string; dc: string; status: string; statusKind: string; severity: string; sevKey: string; env: string;
  sources: Option[]; public: boolean; hub: string; occurrences: number; firstSeen: number; last: number; resolved: boolean; owner: string;
  problem?: { id: number; label: string; title: string };
};
export type Incidents = {
  empty?: boolean; emptyText?: string; tiles: Tile[]; facets: Facet[]; list: ListState; rows: IncidentRow[]; room: string; findingsWord: string;
  bulk: null | { problems: Option[]; back: string }; banners: Banner[]; refresh: boolean;
};
export type RcaRef = { id: number; title: string; href: string; status: string; statusKind: string; at: number; source: string; auto: boolean };
export type Incident = {
  id: number; title: string; status: string; statusKind: string; severity: string; sevKey: string; env: string; owner: string; occurrences: number; lastSeen: number;
  actions: string[]; task: Seg; admin: boolean; banners: Banner[]; prod: boolean; lazy: Lazy | null;
  source: { tags: Option[]; messages: { body: string; sender: string; mxid: string; meta: Seg[]; disposition: string }[]; lines: string[]; note: string };
  reports: RcaRef[]; deep: null | { can: boolean; message: string }; fix: Fix | null; timeline: TimelineEv[];
  rca: null | (MdDoc & { run: number; title: string; folded: boolean }); facts: Fact[]; problem: ProblemPanel | null; refresh: boolean;
};
export type Findings = {
  word: string; room: string; files: Seg[];
  findings: { at: number; severity: string; sevKind: string; env: string; title: string; suspicious: boolean; href: string }[];
  runs: { at: number; status: string; summary: string; href: string; mr: string }[];
};
export type IncidentRun = { id: number; at: number; status: string; md: MdDoc; alerts: { since: string; severity: string; name: string; target: string }[] };
export type Finding = {
  title: string; severity: string; sevKind: string; env: string; at: number; suspicious: boolean; prod: boolean; summary: string; sources: string[];
  lazy: Lazy | null; md: MdDoc; fix: Fix; problem: ProblemPanel | null; refresh: boolean;
};
export type Alerts = {
  loaded: boolean; host: string; warn: string; firing: number; pending: number; fetchedAt: number; tiles: Tile[]; facets: Facet[]; list: ListState; sites: string;
  rows: { name: string; pending: boolean; summary: string; severity: string; sevKind?: string; since: number; activeAt: string; dc: string; instance: string; value: string; rca: string }[];
};
export type ProblemRow = {
  id: number; label: string; title: string; status: string; statusWord: string; statusKind: string; linked: string; env: string; severity: string;
  public: boolean; age: string; owner: string; rootCause: string; rootLabel: string; mergedInto?: number;
};
export type Problems = {
  open: number; issues: number; suggested: number; resolved7: number; autoLine: string; tab: string; tabs: { key: string; label: string; count: number; href: string; on: boolean }[];
  facets: Facet[]; list: ListState; rows: ProblemRow[]; hasAny: boolean; canCreate: boolean; heading: string; banners: Banner[]; refresh: boolean;
  backfill: null | { notes: string[]; rows: Seg[][] };
};
export type ProbLink = { id: number; item: ProbItem; secondary: boolean; source: string; by: string; at: number; why: string; state: string; canEdit: boolean };
export type Problem = {
  id: number; label: string; title: string; status: string; statusWord: string; statusKind: string; rootLabel: string; rootCause: string; workaround: string; owner: string; env: string;
  explains: string; fixSince: number | null; mergedInto: number; prod: boolean; banners: Banner[]; lazy: Lazy | null; linkedCount: number;
  groups: { key: string; title: string; links: ProbLink[] }[]; incoming: { id: number; label: string; title: string; word: string }[];
  suggestions: ProbSuggestion[]; gone: ProbLink[]; timeline: TimelineEv[]; fix: Fix | null; facts: Fact[]; back: string; refresh: boolean;
  controls: null | { moves: Option[]; canResolve: boolean; issues: { id: number; title: string; quiet: boolean; state: string }[] };
};
export type RcaList = {
  count: number; notMerged: number; facets: Facet[]; list: ListState;
  rows: { at: number; title: string; href: string; file: string; incident: number; status: string; statusKind: string; source: string; auto: boolean; cost: string; mr: string }[];
};
export type RcaItem = {
  id: number; title: string; status: string; statusKind: string; at: number; incident: number; banners: Banner[]; facts: Fact[]; outline: Option[]; lazy: Lazy | null;
  md: MdDoc; fix: Fix; tasks: null | { title: string; items: { n: number; title: string; task: Seg }[] }; problem: ProblemPanel | null; refresh: boolean;
};
export type RcaFile = { title: string; file: string; lazy: Lazy | null; md: MdDoc; fix: Fix | null; outline: Option[] };
export type TodayIncidents = {
  total: number; open: number; rows: { id: number; title: string; status: string; sevKey: string; env: string; last: number }[];
  top: null | { id: number; title: string; status: string; sevKey: string; env: string; firstSeen: number; ackedAt: number; ackedBy: string; stageSince: number; stageFor: string };
};
export type TodayChecks = { hours: { at: number; ok: number; fail: number }[]; total: number; failed: number; worst: number };

// The Infrastructure section.
export type PillData = { text: string; kind: string; title?: string };
export type ScanBar = { tasks: string; back: string; label: string; busy: boolean; meta: string; title: string; overdue: string };
export type Reload = { seconds: number; url?: string } | null;
export type MeterData = { pct: number; figure: string; kind: string; title: string } | null;
export type MiniData = { area: string; line: string; rule?: string; label: string };
export type RunStrip = { mids: string[]; marks: { x: string; kind: string; title: string }[]; label: string; hours: number };

export type InventoryRow = {
  id: number; name: string; ip: string; ipMore: number; hit: string; flags: Option[]; env: string; dc: string;
  health: { word: string; kind: string; detail: string; title: string }; services: string; servicesFail: boolean; dead: number; cve: string;
  analysis: { note?: string; href?: string; title?: string; at?: number; who?: string } | null;
  vcpu: string; ram: string; used: string; disk: string; uptime: string;
};
export type Inventory = {
  goto?: string; needs: number; total: number; servicesAt: number; pressure: string; factsAt: number; tiles: Tile[]; facets: Facet[];
  list: ListState; rows: InventoryRow[]; csv: string; scan: ScanBar; refresh: boolean;
};
export type VmReq = { can: boolean; busy: boolean; note: string; title?: string };
export type Share = { unit: string; pct: number; figure: string; share: string; note: string };
export type UpstreamRow = { port: number; state: PillData; unit: string; routes: Option[]; more: number };
export type VmServiceRow = {
  unit: string; kind: string; why: string; verdict: PillData; memory: string; memoryNote: string; cpu: string; restarts: string; up: string;
  ports: string[]; deadPorts: string[]; errors: string; routes: string[];
};
export type VmAnalysis = {
  latest: {
    title: string; severity: string; severityKind: string; at: number; who: string; mode: string; cost: string; href: string; prod: boolean;
    board: Seg; lazy: Lazy | null; summary: string; report: MdDoc; folded: boolean; fix: Fix | null; more: { href: string; title: string; severity: string }[];
  } | null;
  history: { at: number; status: PillData; report: string; href: string; who: string; trigger: string; cost: string }[];
};
export type VmVulns = {
  note: string;
  components: { priority: PillData; href: string; name: string; version: string; kev: boolean; kind: string; vulns: number; fixed: string }[];
  services: { priority: PillData; service: string; href: string; units: string; vulns: number; kev: number; exposure: string; components: Seg[] }[];
  containers: { name: string; image: string; declared: boolean; source: string; inside: string; vulns: number }[];
};
export type ConnectStep = { label: string; cmd: string };

export type InventoryItem = {
  connect?: ConnectStep[] | null;
  connectVia?: string;
  id: number; name: string; env: string; dc: string; summary: string; state: string;
  actions: { refresh: VmReq; collected: number; analyse: VmReq | null }; banners: Banner[];
  resources: { label: string; figure: string; unit: string; state: string; kind: string; note: string; title: string }[];
  ready: { figure: string; of: string; note: string; hot: boolean }; jump: Option[];
  incidents: { id: number; title: string; status: string; kind: string }[]; serviceCount: string;
  services: { rows: VmServiceRow[]; idle: string[]; source: string };
  upstreams: { count: number; deadCount: number; dead: UpstreamRow[]; ok: UpstreamRow[]; repo: UpstreamRow[] }; serves: string; dataRoles: string;
  analysis: VmAnalysis; problem: ProblemPanel | null; vulns: VmVulns; consumers: { memory: Share[]; cpu: Share[] } | null;
  pressure: { name: string; state: string; kind: string; why: string; rows: { label: string; limits: string; now: string; hot: string; max: string; day: MiniData | null; week: MiniData | null }[] }[];
  mounts: { mount: string; size: string; pct: number; kind: string; used: string; inodes: string; growth: string; days: string }[];
  changes: { at: number; field: string; old: string; new: string }[]; raw: Option[]; identity: Fact[]; access: Fact[]; notes: string[]; refresh: Reload;
};

export type CapacityFs = {
  level: PillData; new: boolean; name: string; host: string; href: string; env: string; dc: string; mount: string; used: MeterData; free: string; growth: string; days: string;
  analysis: { fs: string; note: string; report: { href: string; title: string; at: number; who: string; board: Seg | null } | null; can: boolean; show: boolean; msg: string };
};
export type CapacityVm = {
  name: string; href: string; env: string; bits: string; hit: string; level: PillData; monitored: boolean; why: string;
  disk: { mount: string; sub: string; used: MeterData; filesystems: { mount: string; used: string; growth: string; days: string }[] } | null;
  cpu: { text: string; hot: string; sub: string } | null; memory: { text: string; hot: string; sub: string } | null;
};
export type Capacity = {
  sub: string[]; banners: Banner[]; tiles: Tile[]; hasVMs: boolean; show: { level: string; risk: number; all: number; riskHref: string; allHref: string };
  filesystems: CapacityFs[]; hot: CapacityVm[]; hotNote: string; vmTotal: number; rows: CapacityVm[]; list: ListState; facets: Facet[]; scan: ScanBar; refresh: Reload;
};

export type Hygiene = {
  loaded: boolean; at: number; counters: string; banners: Banner[]; tiles: Tile[]; scan: ScanBar; refresh: boolean;
  sections: { title: string; note: string; suggestion: string; whenLabel: string; rows: { alert: string; severity: string; dc: string; instance: string; why: string; neverResolved: boolean; when: string; detail: string }[] }[];
};

export type CertRow = { status: PillData; label: string; issuer: string; pct: number; level: string; days: string; expires: string; names: { name: string; mismatch: boolean }[]; mismatches: number };
export type Certs = {
  ran: boolean; scan: ScanBar; refresh: boolean; at?: number; summary?: string; status?: PillData; names?: number; certificates?: number; banners?: Banner[];
  hosts: { status: PillData; host: string; pct: number; level: string; days: string; expires: string; issuer: string }[] | null;
  hot?: CertRow[]; ok?: CertRow[]; stale?: CertRow[]; unreachable?: Option[]; host?: string;
};

export type Checks = {
  ok: number; total: number; bad: string[] | null; failing: number; banners: Banner[]; cost: string; runs: string; scan: ScanBar; refresh: boolean;
  exceptions: { rows: { pill: PillData; title: string; href: string; kind: string; meta: string }[]; total: number };
  checks: { task: string; name: string; href: string; status: PillData; busy: boolean; last: string; at: number; summary: string; state: string; title: string; strip: RunStrip }[];
  credentials: { title: string; rows: { name: string; href: string; detail: string; expires: string }[]; more: number; line: string; href: string } | null;
  readiness: { title: string; date: string; when: string; rows: { status: PillData; check: string; detail: string }[]; gaps: string[] | null } | null;
  selfcheck: { status: PillData; line: string; bad: SelfRow[]; ok: SelfRow[]; at: string; kuma: string };
};
export type SelfRow = { status: PillData; name: string; group: string; detail: string };

export type Restore = { result: string; kind: string; date: string };
export type Accepted = { id: number; by: string; scope: string; reason: string; canUndo: boolean };
export type CoverItem = { risk: string; kind: string; title: string; meta: string; board: Seg };
export type BackupRow = { status: PillData; name: string; href: string; age: string; size: string; ratio: string; kinds: string; newest: string; restore: Restore; accepted: Accepted | null; mark: boolean };
export type Backups = {
  ran: boolean; admin: boolean; templates: { url: string; repo: string }; plans: { id: number; state: PillData; name: string; engine: string; env: string; server: string; schedule: string; destination: string; restore: Restore }[];
  coverage: {
    generated: string; host: string; diskPct: string; pct: number; diskKind: string; used: string; total: string; issue: string; tiles: Tile[];
    high: CoverItem[]; gaps: CoverItem[]; unencrypted: CoverItem[]; noscript: CoverItem[];
    families: { id: string; name: string; sets: number; size: string; engine: string; how: string; defects: string; issues: string }[];
  };
  scan: ScanBar; refresh: boolean; banners: Banner[]; status?: PillData; sets?: number; failing?: number; accepted?: number; at?: number; summary?: string;
  facets?: Facet[]; list?: ListState; rows?: BackupRow[]; okRows?: BackupRow[];
};
export type BackupSet = {
  name: string; empty: boolean; banners: Banner[]; status?: PillData; accepted?: Accepted | null; real?: string; override?: { check: string; openDays: number; maxDays: number } | null;
  checks?: number; latest?: string; rows?: { at: number; status: PillData; age: string; size: string; newest: string }[];
  chart?: { points: [number, number][]; latest: string; min: string; max: string; low: number; high: number; from: number; to: number } | null;
};
export type BackupNew = {
  values: Record<string, string>; draft: string; current: { uriRedacted: string; sha: string; token: string }; admin: boolean; tested: boolean;
  test: { state: string; member: string; reason: string; missing: string; facts: Fact[]; absent: string[] }; derived: Fact[]; members: string[]; member: string; topology: string;
  targets: Option[]; target: string; noTarget: boolean; schedules: { key: string; label: string; runs: string[]; error: string; note: string }[]; tz: string; repo: string; note: string; prod: boolean;
  replace: string; banners: Banner[]; refresh: Reload;
};
export type BackupInstall =
  | { mode: "operator"; command: string; after: string; host: string; installedAt: number | null }
  | { mode: "none"; server: string }
  | { mode: "running"; by: string; at: number }
  | { mode: "done"; ok: boolean; by: string; at: number; secret: string; reason: string; steps: { kind: string; name: string; result: string; detail: string }[] };
export type BackupPlan = {
  id: number; name: string; state: PillData; engine: string; env: string; server: string; schedule: string; prod: boolean; banners: Banner[];
  installBtn: { label: string; busy: boolean; again: boolean } | null; fixerHref: string; mrHref: string; taskHref: string; warnings: string[]; overlaps: string[];
  install: BackupInstall | null; plan: string; jobPath: string; files: { path: string; lines: number; text: string }[]; fixPrompt: string;
  tests: { date: string; result: Restore; object: string; notes: string; by: string }[]; testForm: { today: string } | null; record: Fact[]; repo: string; refresh: Reload;
};

// --- Delivery ---
export type CicdFinding = {
  id: number; title: string; kind: string; klass: string; severity: string; status: string; env: string; lastSeen: number; firstSeen: number; resolvedAt: number;
  resolveNote: string; occurrences: number; project: string; projectId: number; job: string; author: string; mr: string; detail: string; proposed: string; task: Seg;
};
export type CicdCell = { env: string; branch: boolean; status?: PillData | null; sha?: string; at?: number; deployedAt?: number; regression?: boolean; tags?: string[] };
export type ReadinessLink = { href: string; pill: PillData | null };
export type CicdRow = { id: number; name: string; executor: string; include: string; readiness: ReadinessLink; cells: CicdCell[] };
export type EnvCard = {
  env: string; branches: number; success: number; href: string; bar: { state: string; x: number; w: number }[]; barLabel: string;
  rows: { state: string; label: string; count: number; href: string; hot: boolean; note: string }[];
};
export type Cicd = {
  banners: Banner[]; tiles: Tile[]; attention: CicdFinding[]; attentionTotal: number; failing: number; onRelease: number; stuck: number;
  envs: { cards: EnvCard[]; noun: string; kind: string; count: number; engine: string; days: number; since: string } | null; kindLinks: Seg[];
  facets: Facet[]; list: ListState; rows: CicdRow[]; pick: { count: number; noun: string; env: string; state: string; clear: string } | null;
  engine: string; writes: string; readiness: string | null; placeholder: string;
};
export type CicdFailures = {
  open: number; onRelease: number; view: string; tabs: { label: string; count: number; href: string; on: boolean }[]; facets: Facet[]; list: ListState;
  rows: CicdFinding[]; noTools: boolean;
};
export type CicdStuck = {
  rows: CicdFinding[]; waiting: CicdFinding[]; running: { projectId: number; project: string; id: number; sha: string; at: number }[]; minutes: string; engineId: string;
};
export type CicdAudit = {
  branchFindings: number; mode: string; auditedAt: number; secTop: CicdFinding[]; secRest: CicdFinding[]; secCount: number;
  sections: { rule: string; title: string; count: number; lead: CicdFinding[]; more: CicdFinding[] }[]; variables: CicdFinding[]; varsNote: string; engine: string;
};
export type CicdProject = {
  name: string; path: string; cells: CicdCell[]; findings: CicdFinding[]; rail: Fact[];
  deploys: { at: number; env: string; sha: string; author: string; hosts: string; verified: PillData }[];
  pipelines: { id: number; env: string; status: PillData; sha: string; at: number; bridge: string }[];
};
export type CicdItem = {
  id: number; title: string; status: PillData; prod: boolean; detail: string; writes: string; evidence: string; task: Seg; rail: Fact[]; links: Seg[]; problem: ProblemPanel | null;
  proposals: { text: string; call: string; curl: string; rule: string; needsToken: boolean }[];
};
export type RepoCheckRow = {
  status: PillData; title: string; id: string; severity: string; evidence: string; where: string; fix: { file: string; text: string; why: string; source: string } | null;
};
export type RepoPipeline = { env: string; branch: boolean; protected?: boolean; devPush?: boolean; status?: PillData | null; extra?: string; sha?: string; at?: number };
export type RepoReport = {
  verdict: PillData; blockers: number; warnings: number; passed: number; summary: string[]; checkedAt: number; error: string; open?: RepoCheckRow[]; rest?: RepoCheckRow[];
  pipelines?: RepoPipeline[]; rail?: Fact[]; pipelinesFrom?: string; fix?: Fix;
};
export type RepoIndex = {
  mode: "index"; value: string; error: string; placeholder: string; tiles: Tile[]; checked: number; facets: Facet[]; list: ListState; engine: string; banners: Banner[];
  rows: { project: string; href: string; verdict: PillData; blockers: number; warnings: number; at: number; by: string }[];
};
export type RepoPage = {
  mode: "repo"; target: string; name: string; path: string; banners: Banner[]; placeholder: string; refresh: boolean;
  check: { can: boolean; busy: boolean; msg: string; label: string }; report: RepoReport | null;
};
export type VulnExposure = { word: string; hot: boolean; via: string };
export type VulnAffected = { host: string; hostHref: string; env: string; service: string; serviceHref: string; unit: string; exposure: VulnExposure; version: string; how: string };
export type VulnGroupRow = {
  href: string; pri: string; priKind: string; name: string; versions: string; kev: number; internet: boolean; kind: string; ecosystem: string; class: string; vulns: number;
  targets: number; fixed: string; due: string; late: boolean; affects: string; affected: VulnAffected[]; moreAffected: number; moreHref: string;
};
export type VulnCoverage = {
  scanned: number; hosts: number; withAccess: number; kinds: string; mode: string; feedHosts: { host: string; state: string; requests: number; error: string }[];
  missing: { name: string; env: string; why: string }[];
  queries: { line: string; requests: string; nvd: { product: string; state: string; cves: string; pages: number; why: string }[]; failed: { kind: string; subject: string; why: string }[] } | null;
  containers: {
    note: string; blindCounts: string; rows: { host: string; hostHref: string; env: string; name: string; image: string; digest: string; source: string; inside: string }[];
    blind: { label: string; subject: string; host: string; hostHref: string; why: string }[];
  } | null;
};
export type Vulns = {
  scanned: boolean; host: string; p1: number; p2: number; kev: number; components: number; feedAt: string; banners: Banner[]; tiles: Tile[]; fixFirst: VulnGroupRow[];
  risk: { service: string; href: string; on: boolean; hosts: number; hostList: string; p1: number; p2: number; kev: number; exposure: VulnExposure; fix: Seg | null }[];
  rows: VulnGroupRow[]; total: number; more: Seg | null; empty: string; filtered: boolean; sel: Record<string, string>;
  choices: { env: string[]; pri: string[]; kind: string[]; svc: string[] }; coverage: VulnCoverage;
};
export type VulnGroup = {
  key: string; name: string; pri: string; priKind: string; kind: string; ecosystem: string; class: string; open: number; targetCount: number; affects: string; banners: Banner[];
  summary: Lazy | null; problem: ProblemPanel | null; more: Seg | null; affected: VulnAffected[]; fix: Fix | null; canSuppress: boolean;
  vulns: { pri: string; priKind: string; score: string; id: string; href: string; kev: boolean; suppressed: boolean; title: string; why: string; affects: string; severity: string; fixed: string; due: string }[];
  targets: { target: string; href: string; version: string; env: string }[];
  rules: { id: string; vuln: string; component: string; target: string; env: string; expires: string; by: string; reason: string }[];
  suppress: { vuln: string; component: string; env: string; until: string } | null;
};

// --- Work: the board, the task page, New task ---
export type Chip = { label: string; href: string; on: boolean; title?: string };
export type FilterRow = { label: string; chips: Chip[]; folds: { summary: string; open: boolean; chips: Chip[] }[] };
export type BoardFilters = { main: FilterRow[]; more: FilterRow[]; advanced: number; active: Chip[]; clear: string };
export type MoveCtl = { kind: "task" | "jira"; task?: number; key?: string; options: Option[] };
export type BadgeData = { text: string; kind: string; title?: string };
export type CardPlan = { wait?: string; env?: string; kind?: string; summary?: string; writes?: { text: string; targets: string[] }[]; more?: number; meta?: string[]; open?: number };
export type BoardCard = {
  ref: string; tag: string; title: string; href: string; owner: string; open: boolean; priority: string; priorityHigh: boolean; env?: string;
  source?: Seg | null; auto?: boolean; verb?: string; at?: number; age?: string; fix?: BadgeData | null; agent?: BadgeData | null; plan?: CardPlan | null;
  badges: BadgeData[]; fixPrompt?: boolean; move?: MoveCtl;
};
export type BoardCol = {
  id: string; name: string; count: number; cards: BoardCard[]; first: boolean; empty: string; help: string; title?: string; limits?: string; over?: boolean; under?: boolean;
  fixer?: boolean; more?: { hidden: number; href: string };
};
export type MoveChoice = { key: string; col: string; options: Option[] } | null;
export type MatrixWait = { pid: string; title: string; requester: string; forwarded: string; hoursLeft: number; project: string; summary: string; assignee: string; options: Option[] };
export type Board = {
  mode: "jira" | "local"; tracker: string; status: Seg[][]; columns: BoardCol[]; readOnly: string; canCreate: boolean; canRefresh?: boolean; boardError?: string; note?: string;
  local?: string; connectHref?: string; chips?: Chip[]; filters?: BoardFilters; results?: string; emptyAll?: { title: string; text: string }; choice?: MoveChoice;
  notices: Banner[]; matrix: MatrixWait[]; tabs: { label: string; href: string; current: boolean }[]; newHref: string; return: string; ask: boolean; admin: boolean;
  refresh: { seconds: number; url?: string } | null;
};
export type ChatMsg = { seq: number; who: string; argus: boolean; at: number; body: string; proposal: boolean };
export type Chat = { key: string; messages: ChatMsg[]; pending: boolean; canPost: boolean; ask: boolean; empty?: string; note?: string; pause?: Seg };
export type SecretForm = { name: string; hosts: string; purpose: string; ttl: number; label: string };
export type Secrets = { items: { name: string; text: string; note?: string; warn?: string; rotate?: boolean }[]; forms: SecretForm[]; new: SecretForm | null; note?: string };
export type AgentResult = {
  summary: string; evidence: string[]; rollback: string[]; needs: string[]; mr?: string; fixRun?: string; report?: MdDoc; moves?: { col: string; label: string; primary: boolean }[];
};
export type AgentPlan = {
  summary: string; env: string; kind: string; production: boolean; prodNote: boolean; estimate?: string; steps: { text: string; mode: string; targets: string[] }[]; fix?: Fix; needs: string[];
};
export type Agent = {
  key: string; state: string; label: string; kind: string; meta: string[]; admin: boolean; finished: boolean; chatOn: boolean; planVersion: number;
  replan?: { can: boolean; why: string; quiet: boolean }; notices?: Banner[]; facts?: Fact[];
  progress?: { label: string; cur: number; bar: boolean; now: Seg[]; meta: Seg[][]; fix: boolean; steps?: string[] };
  result: AgentResult | null; plan?: AgentPlan; questions: { id: string; text: string; blocking: boolean; answer: string }[]; emptyDesc: boolean; running: boolean;
  inputs?: { instructions: string; postJira: boolean; tracker: string; paused: boolean }; pause?: Seg;
  spin?: { can: boolean; label: string; reason: string; primary: boolean; proposal: boolean }; chat: Chat | null; secrets: Secrets | null;
  log: { at: number; kind: string; text: string }[];
};
export type TaskPage = {
  id: number; ref: string; title: string; tag: string; status: string; statusKey: string; env: string; owner: string; priority: string; openedAt: number; readOnly: string; prod: boolean;
  return: string; gone?: string; move?: MoveCtl; assign: boolean; links: Seg[]; description: string; agent: Agent | null; evidence?: { text: string; from: string; fold: boolean }; fixPrompt: string;
  fixer?: { heading: string; notes: Seg[][]; hosts?: { alias: string; checked: boolean }[]; envLabel?: string };
  comments: { count: number; items: { author: string; at: number; body: string; pending: boolean }[]; blocked: string; max: number; tracker: string };
  history: { at: number; kind: string; tag: string; actor: string; text: string }[]; labels: string[]; record: Fact[]; notices: Banner[]; choice: MoveChoice; refresh: { seconds: number; url?: string } | null;
};
export type NewTask = {
  exists?: Seg; src?: string; srcId?: string; values: Record<string, string>; envs: Option[]; priorities: string[]; return: string; sub: string; blocked: string;
  limits: { title: number; text: number; prompt: number }; trackers?: { jira: string; gitlab: string; projects: Option[]; project: string };
};

// --- Work: Conversations and Ask Argus ---
export type QaMsg = {
  id: number; at: number; sender: string; mxid: string; text: string; edited: boolean; long: boolean; reconstructed: boolean; reason: string; disp: BadgeData | null; room: string;
  noText?: string; link?: string; chain?: Seg[][];
};
export type QaAsk = { note?: string; head?: string; text?: string; link?: string; answered?: string; tail?: string };
export type QaIssue = {
  id: number; title: string; state: BadgeData | null; env: string; severity: string; confirmed: boolean; suspicious: boolean; reconstructed: boolean; who: string; span: string; missing: string[];
  lazy: Lazy | null; chain: Seg[][]; ask: QaAsk | null; messages: QaMsg[]; count: number; older: number; problem: { id: number; label: string; title: string } | null;
};
export type Qa = {
  view: "issues" | "messages"; ready: boolean; tabs: { label: string; href: string; current: boolean; count: number | null }[]; status: string[]; tiles?: Tile[]; rows?: (QaIssue & QaMsg)[];
  list?: ListState; facets?: Facet[]; empty?: string; room?: string; dates?: Record<string, string>; bulk?: null | { problems: Option[]; back: string };
  notIssues?: { count: number; note: string; rows: QaMsg[] };
};
export type Ask = {
  on: boolean; return: string; chat: Chat | null; thread: boolean; threads: { href: string; first: string; who: string; count: number; at: number }[]; notices: Banner[];
  refresh: { seconds: number; url?: string } | null;
};

export type RunRow = { id: number; at: number; task: string; status: BadgeData | null; seconds: string; cost: string; turns: string; denied: string; summary: string };
export type CostDay = { day: string; usd: number; label: string };
export type Runs = { rows: RunRow[]; list: ListState; facets: Facet[]; day: number; dayBad: number; cost: string; days: CostDay[]; dayTop: number; dayTopLabel: string };
export type RunMsg = QaMsg & { source: string };
export type RunFinding = { href: string; title: string; severity: BadgeData | null; env: string; summary: string };
export type Run = {
  id: number; task: string; at: number; status: BadgeData | null; md: MdDoc; showMessages: boolean; messages: RunMsg[]; findings: RunFinding[]; findingsWord: string;
  incidents: { text: string; href: string }[]; rail: Fact[];
};
export type Knowledge = { count: number; md: string; services: string[]; admin: boolean };
export type FixRow = { id: string; at: number; env: string; summary: string; by: string; status: BadgeData | null };
export type FixList = { rows: FixRow[]; state: string; envs: string; notices: Banner[]; steps: string[] };
export type FixWait = { text: string; detail: string; kind: string; steps: string[] };
export type FixReport = {
  status: BadgeData; outcome: string; at: number; cost: string; turns: string; denied: string; summary: string; rollback: string; actions: string[][]; backups: string[][];
  verification: string[][]; md: string; prompt: string; promptSha: string; problem?: { text: string; href: string };
};
export type FixRun = { id: string; env: string; hosts: string[]; by: string; refresh: boolean; wait: FixWait | null; report: FixReport | null };
export type FixReview = {
  env: string; title: string; source: Record<string, string>; ready: boolean; steps: string[]; notices: Banner[]; back: { text: string; href: string };
  hosts?: string[]; prompt?: string; maxBytes?: number; preamble?: string;
};

export type IdeaRow = {
  id: number; status: BadgeData; title: string; at: number; area: string; adviser: string; adviserRole: string; initial: string; effort: string; value: string; evidence: Seg[]; facts: Option[];
  actions: string[]; prompt: string; promptHref?: string; refined?: boolean; wait: boolean; revives?: number; built?: { ref: string; by: string; when: string; href?: string };
};
export type IdeaAdviser = { key: string; name: string; initial: string; meta: string[]; can: boolean; why: string };
export type Ideas = {
  admin: boolean; line: string[]; tabs: { label: string; href: string; current: boolean; count: number }[]; status: string; total: number; heading: string; list: ListState; rows: IdeaRow[]; empty: string;
  banners: Banner[]; refresh: boolean; facets: Facet[]; advisers: IdeaAdviser[]; reason: { min: number; max: number };
};

export type ChipN = { label: string; href: string; on: boolean; count: number };
export type ProposalRow = {
  id: number; title: string; severity: BadgeData | null; at: number; env: string; meta: string[]; source: Seg; why: string; description: string; evidence: string; fixPrompt: string; facts: Option[];
  priority: string; labels: string; suggested: string; snooze: "" | "ok" | "expiring";
};
export type ProposalDecided = { id: number; at: number; title: string; severity: BadgeData | null; source: Seg; state: BadgeData; by: string; reason: string; task?: Seg };
export type ProposalConfirm = {
  action: string; cancel: string; stop?: string; head: string; body: string[]; open: { title: string; severity: BadgeData | null; env: string }[]; ids: number[]; gone: Option[]; skipped: number;
  button: string; danger: boolean; block: string; warn: string; reason?: number;
};
export type ProposalAccept = {
  noun: string; users: Option[]; project: string; projects: string[]; priorities: string[]; tracker: string; maxTitle: number; button: string; note: string;
  trackers?: { jira: string; gitlab: string; projects: Option[]; project: string };
};
export type Proposals = {
  view: string; pending: number; autoOn: string[]; expiryDays: number; tiles: Tile[]; tabs: { label: string; href: string; current: boolean; count: number }[]; rejectedToday: number;
  legacy: { key: Seg; title: string; href: string; source: Seg | null; status: string; gone: boolean; env: string; at: number }[]; banners: Banner[]; here: string; q: string; search: [string, string][];
  chips: { label: string; chips: ChipN[] }[]; sorts: Option[]; sort: string; filtered: boolean; clear: string; settings: { auto: { key: string; label: string; on: boolean }[]; expiryDays: number; rejectDays: number; note: string };
  rows: ProposalRow[]; decided: ProposalDecided[]; confirm: ProposalConfirm | null; total: number; matched: number; maxReason: number; refresh?: boolean;
  pager: { page: number; pages: number; matched: number; from: number; to: number; prev?: string; next?: string } | null; picked?: boolean; bulk?: [string, string][]; selHref?: string; accept?: ProposalAccept; bulkNoun?: string;
};

export type HelpNode = { kind: "md" | "h3" | "details" | "admin"; text: string; id?: string; kids?: HelpNode[] };
export type HelpSection = { id: string; title: string; admin: boolean; nodes: HelpNode[] };
export type HelpDoc = { slug: string; title: string; summary: string; admin: boolean; intro: HelpNode[]; sections: HelpSection[] };
export type HelpHit = { href: string; page: string; section: string; snippet: { text: string; mark: boolean }[] };
export type HelpIndex = {
  q: string; maxQuery: number;
  results?: { count: number; shown: number; rows: HelpHit[] };
  cards?: { href: string; title: string; summary: string; admin: boolean; sections: Option[] }[];
  home?: HelpDoc;
};
export type HelpPage = { doc: HelpDoc; pages: { slug: string; title: string; href: string }[]; maxQuery: number };
export type HelpAll = { docs: HelpDoc[] };

export type LessonRow = {
  id: number; state: string; stateKind?: string; title: string; body: string; service: string; env: string; kind: string; tags: string[]; source: Seg;
  proposedAt: number; evidence: string; actions: string[]; editable: boolean; review: string; supersedes?: number;
  used?: { uses: number; last: number }; decided?: { by: string; at: number };
};
export type Lessons = {
  view: string; title: string; waiting: number; on: boolean; tabs: { label: string; count: number; current: boolean; href: string }[]; facets: Facet[]; list: ListState;
  rows: LessonRow[]; empty: string; banners: Banner[]; refresh?: boolean; envs: string[]; kinds: string[]; max: { title: number; body: number }; here: string;
};
export type LinkConfirm = {
  error?: string; note?: string; back: string; action?: string; head?: string; button?: string; body?: string[]; maxNote?: number; skipped?: number; links?: number[];
  items?: { confidence: string; item: string; linkWord: string; problem: string; title: string }[]; gone?: { item: string; problem: string; state: string }[];
};
export type Links = {
  line: string[]; can: boolean; rejectDays: number; any: boolean; maxNote: number; problem: { id: string; label: string } | null; confirm: LinkConfirm | null;
  banners: Banner[]; minConfidence: string; refresh?: boolean; rows?: ProbSuggestion[]; list?: ListState; facets?: Facet[]; picked?: boolean; here?: string; bulk?: [string, string][]; selHref?: string;
};
export type Replies = {
  rows: { pid: string; title: string; requester: string; forwarded: string; hours: number; project: string; summary: string; choices: Option[]; assignee: string }[];
  banners: Banner[]; refresh?: boolean;
};

export type SettingField = { name: string; label: string; type: "check" | "number" | "text" | "date"; on?: boolean; lo?: number; hi?: number; value?: string };
export type AdminGeneral = {
  host: string; qaRoom: string; applied: { kind: "ok" | "fail" | "none"; text: string }; claude: { on: boolean };
  schedules: { task: string; label: string; schedule: string; enabled: boolean; next: string; nextFull: string }[]; options: string[];
  thresholds: SettingField[]; rcaAuto: SettingField[]; readiness: SettingField[]; qa: SettingField[]; notes: { ra: string; rd: string; qa: string };
  sso: { on: boolean; note: string; breakGlass: string };
  credentials: { checkedAt: number; bad: { name: string; detail: string; ok: boolean }[]; ok: { name: string; detail: string; ok: boolean }[] }; log: string[];
  modules?: { key: string; name: string; desc: string; on: boolean; source: "settings" | "config" | "default" }[];
};
export type AdminUser = {
  name: string; you: boolean; role: string; source: string; mfa: boolean; mfaUsed: number; lastLogin: number; matrix: string; state: string; stateKind: string; actions: string[];
};
export type AdminUsers = {
  rows: AdminUser[]; names: string[]; matrix: { bot: string; linked: number; server: string };
  mfa: { required: boolean; fromEnv: boolean; note: string; events: { at: number; actor: string; action: string; outcome: string; target: string; detail: string; reason: string }[] };
};

export type AdminBoard = {
  user: string; host: string; project: string; queued: number; state: Banner[]; problems: string[]; notices: Banner[]; refresh: { seconds: number } | null;
  connection: Fact[]; autoIncidents: boolean; hosts: string;
  shared: null | { facts: Fact[]; url: string; project: string; boardId: string; mode: string; approvers: string; component: string; projects: Option[]; boards: Option[]; components: string[] };
  failed: { at: number; kind: string; key: string; taskId: number; error: string }[];
  retrying: { at: number; kind: string; attempts: number; error: string }[];
  agent: { tick: string; error: string; rwUsers: string[]; fixerTasks: { id: number; key: string; tracker: string; env: string; fixState: string; title: string }[];
    settings: { enabled: boolean; assignees: string; column: string; autoDone: boolean; qaEnabled: boolean; qaThreshold: string; qaCap: number; qaOwner: string; fixerTrigger: string; chat: boolean; secrets: boolean } };
  connections: { user: string; identity: string; host: string; mode: string; projects: string; lastSync: number; lastError: string }[];
};

export type StatusVerdict = { kind: string; badge: string; head: string; detail: string; act: string; href: string; facts: { kind: string; label: string; value: string; href?: string }[] };
type Badge = { text: string; kind: "ok" | "warn" | "fail" | "neutral" };
export type AdminClaude = {
  user: string;
  host: string; notices: Banner[]; refresh: { seconds: number } | null; busy: boolean;
  status: StatusVerdict;
  token: { badge: Badge | null; line: string; why: string; warn: Banner[]; open: boolean };
  egress: { badge: Badge | null; line: string; why: string; summary: string; guard: boolean; blocked: string; fail: string };
  usage: { on: boolean };
};

export type AdminWritingStyle = {
  text: string; custom: boolean; line: string; max: number; bytes: number; notices: Banner[]; refresh: { seconds: number } | null; demo: boolean;
};

export type AdminKube = {
  host: string; notices: Banner[]; refresh: { seconds: number } | null; busy: boolean; status: StatusVerdict;
  clusters: { connect?: ConnectStep[] | null; connectVia?: string; name: string; env: string; badge: Badge | null; line: string; agent: string; test: string; why: string; findings: string[] }[];
  add: { replace: string; env: string; open: boolean }; help: { yaml: string; cmds: string };
};

export type AdminSources = {
  host: string; notices: Banner[]; refresh: { seconds: number } | null; busy: boolean; status: StatusVerdict; count: number; block: "" | "picker" | "add" | "edit";
  panels: { key: string; label: string; managed: string; rows: { key: string; type: string; name: string; env: string; badge: Badge | null; purpose?: { label: string; title: string }; meta: string[]; detail: string; where: string; findings: string[]; canCheck: boolean; change?: string; operator?: boolean; editable?: boolean }[] }[];
  kinds: { key: string; label: string; state: "ok" | "failing" | "config" | "missing"; detail: string; count: number; pages: { label: string; href: string }[]; add: string; hint: string }[];
  types?: { label: string; blurb: string; href: string }[]; coming?: string[];
  form?: { type: string; label: string; blurb: string; mode: string; credLabel: string; credHint: string; username: boolean; credOptional?: boolean; purposes: { value: string; label: string; sub: string }[]; override: boolean; name: string; env: string; url: string; user: string; purpose: string; last4: string; overrideOn: boolean };
};

export type AdminTargets = {
  host: string; notices: Banner[]; refresh: Reload; envs: string[]; age_recipients: number;
  rows: { name: string; type: string; envs: string; endpoint: string; bucket: string; prefix: string; badge: { text: string; kind: string } | null; detail: string; at: string; retention: string; plans: number; ilm: string }[];
};

export type CredExpires = { date: string; pill: { kind: string; text: string }; note: string; rotate: string; rotate_now?: boolean };
export type CredRow = { id: string; name: string; second: string; detail: string; kind: string; used_by: string; where: string; expires: CredExpires; last_used: number; checked: number };
export type AdminCreds = {
  host: string; refresh: Reload; scan: ScanBar; checked: boolean; bad: CredRow[]; ok: CredRow[]; absent: CredRow[];
  howto: { kind: string; note: string; creds: { name: string; steps: string[] }[] }[];
  next?: { name: string; days: number } | null; certs?: string; thresholds?: { warn: number; crit: number; rotate: number; today: number };
};

type Pil = { kind: string; text: string };
export type AdminOutline = {
  host: string; user: string; outline_host: string; notices: Banner[]; refresh: Reload; connected: boolean; mode: string; scope: string; url: string; collection: string;
  hosts: string[]; built: number;
  pages: { service: string; name: string; local: Pil; state: Pil | null; url: string; changed: string; note: string }[];
  connection?: { url: string; user: string; team: string; collection: string; since: string; by: string };
  gitlab: {
    connected: boolean; group: boolean; notices: Banner[]; url: string; project: string; hosts: string[]; default_tracker: string; list_map: string; user_map: string;
    connection?: { gitlab: string; where_kind: string; where: string; web_url: string; board: string; board_id: number; default_project: string; token_user: string; bot: boolean; since: string; by: string };
    boards?: Option[]; projects?: Option[]; lists?: [string, string][]; default_project?: string;
  };
};
export type OutlinePage = {
  service: string; name: string; title: string; mode: string; hash: string; built: string; changed: string; scan: string; blocked: boolean;
  outline: { label: string; url: string }; flags: string[]; diff: { lines: { kind: string; text: string }[]; more: number } | null; body: string; view: string;
};

export type Account = {
  user: string; role: string; source: string; must_change: boolean; min_pw: number; sso?: { issuer: string; client: string }; host?: string; refresh?: Reload;
  mfa?: { enabled: boolean; state: string; kind: string; note: string };
  sessions?: { id: string; device: string; current: boolean; kind: string; ip: string; last_seen: number; created: string }[];
  limits?: { remember_days: number; remember_idle: number; session_hours: number; idle_hours: number };
  jira?: {
    connected: boolean; notices: Banner[]; allowed: string; url: string; mode: string; display?: string; jira_name?: string; jira_url?: string; version?: string; last_sync?: number; last_error?: string;
    projects?: { key: string; name: string; on: boolean }[]; chosen?: string[]; default_project?: string;
  };
  matrix?: { id: string; note: string; placeholder: string; saved: boolean };
};
export type AccountMfa = {
  user: string; local: boolean; reauth: string; auth_ago: string; key_ready: boolean; idp: boolean; required: boolean; reset?: boolean; enabled: boolean; enabled_at: string; last_used: number;
  recovery_left: number; recovery_total: number; next: string;
};

export type CatRun = { source: string; status: string; mode: string; trigger: string; counts: Record<string, number> | null; calls: number; note: string; started_at: number; finished_at: number };
export type CatSummary = {
  verdict: { kind: string; head: string; failed_sources: string[] };
  counts: { services: number; confirmed: number; proposed: number; stale: number; rejected: number; unmapped: number; routes: number };
  last_discovery: number; sources: CatRun[];
};
export type CatReview = {
  items: { id: number; service: string; source: string; type: string; kind: string; locator: string; label: string; origin: string; status: string; reason: string; confidence: number; why: string; run_id?: string }[];
  total: number; counts: { learned: number; discovered: number }; capped: number; min_confidence: number; cap: number;
};
export type CatMetrics = {
  routes: { type: string; confirmed: number; proposed: number; rejected: number; stale: number }[];
  unmapped: { total: number; top: { source: string; kind: string; key: string; label: string; note: string }[] };
};

export type SeoChart = { points: [number, number][]; latest: string; min: string; max: string; low: number; high: number; from: number; to: number } | null;
export type SeoWindow = { requests: number; share: string; p95: string } | null;
export type SeoEdge = { level: PillData; edge: string; host: string; cache: boolean; now: SeoWindow; verified: string; hits: string; day: SeoWindow; week: SeoWindow };
export type SeoFinding = { level: PillData; what: string; evidence: string; action: string };
export type SeoSuggestion = { title: string; page: string; evidence: string; action: string };
export type Seo = {
  empty: boolean; at?: number; level?: PillData; refresh: Reload; banners: Banner[];
  edges?: SeoEdge[]; charts?: { share: SeoChart; p95: SeoChart };
  summary: { findings: SeoFinding[]; fix: number };
  suggestions: { empty: boolean; day?: string; summary?: string[]; groups?: { key: string; title: string; rows: SeoSuggestion[] }[] };
  gsc: { empty: boolean; clicks?: SeoChart; impressions?: SeoChart; position?: SeoChart; urls?: { level: PillData; url: string; coverage: string; lastCrawl: string; crawledAs: string }[] };
};

/** GET /api/v1/needs (ADR-0021 D5): the data-source kinds and whether the asked page is not configured. */
export type NeedSource = { key: string; label: string; settings: string; hint: string };
export type Needs = {
  kinds: { key: string; label: string; settings: string; configured: boolean }[];
  notConfigured?: { sources: NeedSource[] };
};
