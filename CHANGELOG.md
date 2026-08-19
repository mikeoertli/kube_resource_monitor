# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Versioning policy

`krm` is pre-1.0. Until 1.0, the minor version is where breaking changes land:

| Change | Bump |
| --- | --- |
| Breaking change to flags, subcommands, or output shape | minor (`0.2.0` → `0.3.0`) |
| New feature, backward compatible | minor (`0.2.0` → `0.3.0`) |
| Bug fix, no interface change | patch (`0.2.0` → `0.2.1`) |

Two surfaces are worth calling out because they are the ones scripts depend on:

- **`-o json`, `-o csv`, `-o prometheus`.** Fields may be added in any release.
  Fields will not be removed, renamed, or change meaning outside a minor bump,
  and every such change is listed under **Changed** below. Note that optional
  fields are *absent* rather than zero when a value does not exist — an
  undeclared CPU limit means no `cpuLimitMilli` key at all — so decode into a
  type that can tell those apart.
- **Exit codes.** `0` success, `1` error, `2` a threshold breach from
  `krm notify --once --exit-code`. These will not be reassigned.

Terminal rendering, colors, key bindings, and log wording are not part of any
compatibility promise.

## [Unreleased]

Nothing yet.

## [0.1.0] - 2026-08-19

First working version.

### Added

- **Live view.** A full-screen terminal UI that refreshes on an interval, with
  expandable rows, live filtering, sorting, and regrouping without restarting.
  `p` pauses, `+`/`-` change the refresh rate, `?` lists every key.
- **Workload rollup.** Usage summed to the Deployment, StatefulSet, DaemonSet,
  Job, or CronJob that owns a pod, resolved through the intermediate ReplicaSet
  or Job, with per-pod and per-container breakdowns underneath.
- **Grouping** by workload, pod, container, node, namespace, or
  PersistentVolumeClaim, plus kind-restricted views (`-g deployment`,
  `-g statefulset`, `-g daemonset`, `-g job`). Kubectl abbreviations accepted.
- **Requests and limits** shown alongside usage (`--requests`, `--limits`), with
  percentages measured against the limit, falling back to the request and then
  to node allocatable.
- **Color by headroom**, using a severity scale from idle through over-limit.
  Honors `NO_COLOR` and detects whether stdout is a terminal.
- **Volume usage** via `-g pvc`, read from each kubelet's summary endpoint, with
  provisioned capacity from the PVC when node proxy access is unavailable.
- **Notify mode** with threshold rules in relative (`cpu>85%`,
  `mem>90% of request`) and absolute (`cpu>1500m`, `mem>2Gi`) forms, hysteresis
  to stop alerts flapping, `--for` to require a breach to persist, `--repeat` to
  re-notify, and desktop delivery through terminal-notifier, osascript, or
  notify-send, falling back to stdout.
- **Machine-readable output**: `-o json`, `-o csv`, `-o prometheus`.
- **Filtering** by label selector, field selector, and name (regular expression,
  or case-insensitive substring when the pattern is not valid regex).
- **metrics-server detection and install** via `krm install-metrics-server`,
  which distinguishes "not installed" from "installed but not serving" and does
  not advise reinstalling over a deployment that is already there.
- **Context and namespace selection** following kubectl's resolution order, with
  `krm contexts` to list what is available.
- **`--demo` mode** running against a synthetic cluster, so the tool can be
  evaluated, demonstrated, and debugged without one.
- **Brand assets** under `assets/`: an icon, a hero logo, single-color variants,
  a multi-resolution `favicon.ico`, and a macOS `.icns`.

### Notes on correctness

These are choices that differ from a naive implementation, recorded because
they change the numbers you see:

- Pod requests and limits follow the scheduler's rules, not a plain sum over
  containers: init containers contribute their maximum rather than their sum,
  restartable sidecars add to the steady state, and pod overhead is included.
- Node rows measure against allocatable rather than the summed limits of their
  pods, which routinely exceed what the machine has, and prefer node-level
  metrics that include the kubelet and system daemons.
- A pod with no metrics sample renders as `-`, never as `0m`. A percentage with
  no denominator renders as `-`, never as `0%`.

[Unreleased]: https://github.com/mikeoertli/kube_resource_monitor/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/mikeoertli/kube_resource_monitor/releases/tag/v0.1.0
