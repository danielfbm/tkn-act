# Graph Report - .  (2026-05-03)

## Corpus Check
- 100 files · ~69,867 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 885 nodes · 1193 edges · 73 communities detected
- Extraction: 80% EXTRACTED · 20% INFERRED · 0% AMBIGUOUS · INFERRED: 238 edges (avg confidence: 0.81)
- Token cost: 404,818 input · 0 output

## Community Hubs (Navigation)
- [[_COMMUNITY_Tekton Types & Loader|Tekton Types & Loader]]
- [[_COMMUNITY_Agent Guide & Embedded Docs|Agent Guide & Embedded Docs]]
- [[_COMMUNITY_Cross-Backend E2E Harness|Cross-Backend E2E Harness]]
- [[_COMMUNITY_Validate Command & Cluster-Engine Tests|Validate Command & Cluster-Engine Tests]]
- [[_COMMUNITY_Cluster Driver & cmdrunner|Cluster Driver & cmdrunner]]
- [[_COMMUNITY_Cluster Backend Core|Cluster Backend Core]]
- [[_COMMUNITY_Cluster Test Helpers & Fakes|Cluster Test Helpers & Fakes]]
- [[_COMMUNITY_Reporter Events|Reporter Events]]
- [[_COMMUNITY_Docker Backend|Docker Backend]]
- [[_COMMUNITY_Cluster E2E Capture Sink|Cluster E2E Capture Sink]]
- [[_COMMUNITY_Backend Contract & Cluster Exports|Backend Contract & Cluster Exports]]
- [[_COMMUNITY_Exit Codes|Exit Codes]]
- [[_COMMUNITY_CLI Commands & Wiring|CLI Commands & Wiring]]
- [[_COMMUNITY_Reporter Color & CLI Flags|Reporter Color & CLI Flags]]
- [[_COMMUNITY_Engine DAG & Cluster Events|Engine DAG & Cluster Events]]
- [[_COMMUNITY_Engine Core|Engine Core]]
- [[_COMMUNITY_Resolver|Resolver]]
- [[_COMMUNITY_Backend Interfaces|Backend Interfaces]]
- [[_COMMUNITY_cmdrunner Fakes|cmdrunner Fakes]]
- [[_COMMUNITY_Doctor|Doctor]]
- [[_COMMUNITY_List & Discovery|List & Discovery]]
- [[_COMMUNITY_DAG Tests|DAG Tests]]
- [[_COMMUNITY_Backend Fake|Backend Fake]]
- [[_COMMUNITY_Tekton Installer|Tekton Installer]]
- [[_COMMUNITY_Docker Integration Tests|Docker Integration Tests]]
- [[_COMMUNITY_Pretty Reporter Output|Pretty Reporter Output]]
- [[_COMMUNITY_PipelineBackend Type Refs|PipelineBackend Type Refs]]
- [[_COMMUNITY_TimeoutFailed Status Constants|Timeout/Failed Status Constants]]
- [[_COMMUNITY_Agent CLI Contract Concepts|Agent CLI Contract Concepts]]
- [[_COMMUNITY_Sidecars Limitation Fixture|Sidecars Limitation Fixture]]
- [[_COMMUNITY_Step-State Limitation Fixture|Step-State Limitation Fixture]]
- [[_COMMUNITY_Persona B  Sidecars Notes|Persona B / Sidecars Notes]]
- [[_COMMUNITY_Cluster Lazy Provisioning|Cluster Lazy Provisioning]]
- [[_COMMUNITY_Stable Agent Contract|Stable Agent Contract]]
- [[_COMMUNITY_RunTask Unsupported Path|RunTask Unsupported Path]]
- [[_COMMUNITY_Community 37|Community 37]]
- [[_COMMUNITY_Community 38|Community 38]]
- [[_COMMUNITY_Community 39|Community 39]]
- [[_COMMUNITY_Community 40|Community 40]]
- [[_COMMUNITY_Community 41|Community 41]]
- [[_COMMUNITY_Community 42|Community 42]]
- [[_COMMUNITY_Community 43|Community 43]]
- [[_COMMUNITY_Community 44|Community 44]]
- [[_COMMUNITY_Community 45|Community 45]]
- [[_COMMUNITY_Community 46|Community 46]]
- [[_COMMUNITY_Community 47|Community 47]]
- [[_COMMUNITY_Community 48|Community 48]]
- [[_COMMUNITY_Community 49|Community 49]]
- [[_COMMUNITY_Community 50|Community 50]]
- [[_COMMUNITY_Community 51|Community 51]]
- [[_COMMUNITY_Community 52|Community 52]]
- [[_COMMUNITY_Community 53|Community 53]]
- [[_COMMUNITY_Community 54|Community 54]]
- [[_COMMUNITY_Community 55|Community 55]]
- [[_COMMUNITY_Community 56|Community 56]]
- [[_COMMUNITY_Community 57|Community 57]]
- [[_COMMUNITY_Community 58|Community 58]]
- [[_COMMUNITY_Community 59|Community 59]]
- [[_COMMUNITY_Community 60|Community 60]]
- [[_COMMUNITY_Community 61|Community 61]]
- [[_COMMUNITY_Community 62|Community 62]]
- [[_COMMUNITY_Community 63|Community 63]]
- [[_COMMUNITY_Community 64|Community 64]]
- [[_COMMUNITY_Community 66|Community 66]]
- [[_COMMUNITY_Community 67|Community 67]]
- [[_COMMUNITY_Community 68|Community 68]]
- [[_COMMUNITY_Community 70|Community 70]]
- [[_COMMUNITY_Community 71|Community 71]]
- [[_COMMUNITY_Community 72|Community 72]]
- [[_COMMUNITY_Community 73|Community 73]]
- [[_COMMUNITY_Community 74|Community 74]]
- [[_COMMUNITY_Community 75|Community 75]]
- [[_COMMUNITY_Community 76|Community 76]]

## God Nodes (most connected - your core abstractions)
1. `LoadBytes()` - 23 edges
2. `Backend` - 18 edges
3. `NewStore()` - 17 edges
4. `Substitute()` - 16 edges
5. `newRootCmd()` - 14 edges
6. `Substitute()` - 14 edges
7. `runWith()` - 12 edges
8. `Validate()` - 11 edges
9. `ParamValue struct` - 11 edges
10. `Validate()` - 11 edges

## Surprising Connections (you probably didn't know these)
- `newListCmd()` --implements--> `Stable JSON output contract for agents`  [INFERRED]
  cmd/tkn-act/list.go → AGENTS.md
- `newAgentGuideCmd()` --implements--> `Stable JSON output contract for agents`  [INFERRED]
  cmd/tkn-act/agentguide.go → AGENTS.md
- `exitCodeTable()` --implements--> `Stable exit code contract (0,1,2,3,4,5,6,130)`  [INFERRED]
  cmd/tkn-act/helpjson.go → AGENTS.md
- `main()` --implements--> `Stable exit code contract (0,1,2,3,4,5,6,130)`  [INFERRED]
  cmd/tkn-act/main.go → AGENTS.md
- `newDoctorCmd()` --implements--> `Stable JSON output contract for agents`  [INFERRED]
  cmd/tkn-act/doctor.go → AGENTS.md

## Hyperedges (group relationships)
- **Agent-facing commands (doctor, help-json, agent-guide)** — doctor_newdoctorcmd, helpjson_newhelpjsoncmd, agentguide_newagentguidecmd, concept_machine_readable_json_contract [INFERRED 0.85]
- **Per-command JSON DTOs forming the stable agent contract** — doctor_doctorreport, helpjson_helpjson_struct, list_listresult, validate_validateresult, version_versioninfo, cluster_clusterstatus [INFERRED 0.85]
- **Cluster subcommand family sharing k3d.Driver** — cluster_newclusterupcmd, cluster_newclusterdowncmd, cluster_newclusterstatuscmd, cluster_newdriver [EXTRACTED 1.00]
- **Cross-backend fidelity for v1.2 features** — shorttermgoals_track2_backend_parity, fidelityplan_shared_fixture_descriptor, fidelityplan_tekton_reason_mapping, fidelityplan_ephemeral_volume_apply, fidelityplan_task_retry_event_parity, run_mappipelinerunstatus, volumes_applyvolumesources, run_taskruntooutcome [INFERRED 0.85]
- **cluster.Backend.RunPipeline submit→watch→outcome flow** — run_runpipeline, run_ensurenamespace, volumes_applyvolumesources, run_buildpipelinerun, run_watchpipelinerun, run_collecttaskoutcomes, run_taskruntooutcome [INFERRED 1.00]
- **PipelineBackend opt-in contract** — backend_pipelinebackend_interface, backend_pipelinerunresult_struct, backend_taskoutcomeoncluster_struct, cluster_backend_struct, cluster_runtask_unsupported, run_runpipeline [INFERRED 0.85]
- **cross-backend event-shape fidelity (docker live vs cluster post-hoc)** —  [INFERRED 0.85]
- **task policy loop: timeout + retries** —  [EXTRACTED 1.00]
- **k3d driver via injectable cmdrunner** —  [EXTRACTED 1.00]
- **** —  [INFERRED 0.95]
- **** —  [EXTRACTED 1.00]
- **** —  [EXTRACTED 1.00]
- **shared e2e fixture set drives both backend harnesses** —  [INFERRED 0.95]
- **volume store + materialize + configmap-eater fixture** —  [INFERRED 0.85]
- **Manager allocates workspaces, results, and volume scratch dirs** —  [EXTRACTED 1.00]

## Communities

### Community 0 - "Tekton Types & Loader"
Cohesion: 0.03
Nodes (88): Bundle, docSep regex, LoadBytes(), LoadFiles(), loadOne(), splitYAMLDocs(), TestLimitationsFixturesParse, TestLoadFiles (+80 more)

### Community 1 - "Agent Guide & Embedded Docs"
Cohesion: 0.05
Nodes (49): agentGuide (embedded AGENTS.md), AgentGuideContent(), agentguide_data.md (embedded copy), newAgentGuideCmd(), TestAgentGuideEmbedded, AGENTS.md (canonical AI-agent guide), CLAUDE.md (project instructions, mirrors AGENTS.md), clusterStatus (JSON shape) (+41 more)

### Community 2 - "Cross-Backend E2E Harness"
Cohesion: 0.05
Nodes (47): assertEventShape, runFixtureCluster, TestClusterE2E, runFixtureDocker, TestE2E, fixtures.All, Fixture, Fixture.TestName (+39 more)

### Community 3 - "Validate Command & Cluster-Engine Tests"
Cohesion: 0.07
Nodes (30): TestClusterEngineEmitsTaskRetryEvents(), TestClusterEngineNoRetriesEmitsAttempt1(), fakePipelineBackend, TestEngineRetriesAllFail(), TestEngineRetriesUntilSuccess(), TestEngineTaskTimeout(), TestEngineTimeoutNotRetried(), recBackend (+22 more)

### Community 4 - "Cluster Driver & cmdrunner"
Cohesion: 0.06
Nodes (20): Driver, Status, cannedResponse, Fake, fakeRunner, cmdrunner.New, cmdrunner.NewFake, real (+12 more)

### Community 5 - "Cluster Backend Core"
Cohesion: 0.07
Nodes (21): Backend, ClientBundle, New(), Options, TestTaskRunToOutcomeFailedNoCondition(), TestTaskRunToOutcomeNoRetries(), TestTaskRunToOutcomeTimeout(), TestTaskRunToOutcomeWithResults() (+13 more)

### Community 6 - "Cluster Test Helpers & Fakes"
Cohesion: 0.1
Nodes (35): NewWithClients(), NewWithClientsAndStores(), keysOf(), TestRunPipelineConstructsExpectedResources(), fakeBackend(), flipStatusUntilStop(), keysFromMap(), taskRunObj() (+27 more)

### Community 7 - "Reporter Events"
Cohesion: 0.06
Nodes (31): Event, EventKind type, NewLogSink(), NewTee(), EventKind, EvtStepLog, Reporter interface, jsonSink struct (+23 more)

### Community 8 - "Docker Backend"
Cohesion: 0.09
Nodes (29): Backend, parseCPU(), parseMemory(), scan(), streamLogs(), substituteStepRefs(), Options, substituteSpec() (+21 more)

### Community 9 - "Cluster E2E Capture Sink"
Cohesion: 0.07
Nodes (21): captureSink, assertEventShape(), runFixtureCluster(), TestClusterE2E(), runFixtureDocker(), TestE2E(), SetResultsDirProvisioner(), TestEngineFailurePropagation() (+13 more)

### Community 10 - "Backend Contract & Cluster Exports"
Cohesion: 0.06
Nodes (38): RetryAttempt, TaskOutcomeOnCluster (cluster-side per-task summary), ApplyVolumeSourcesForTest (test-export), cluster.Options (with ConfigMaps/Secrets stores), BuildPipelineRunObject (test-export), CollectTaskOutcomesForTest (test-export), Dynamic client + unstructured.Unstructured for Tekton CRDs, Idempotent Tekton installer (CRD presence check) (+30 more)

### Community 11 - "Exit Codes"
Cohesion: 0.07
Nodes (23): TestWrapAndFrom(), Generic = 1, OK = 0, Error, From(), TestFromNil(), TestFromPlainError(), TestFromWrappedError() (+15 more)

### Community 12 - "CLI Commands & Wiring"
Cohesion: 0.09
Nodes (26): AgentGuideContent(), newAgentGuideCmd(), TestAgentGuideEmbedded(), newClusterCmd(), newClusterDownCmd(), newClusterStatusCmd(), newClusterUpCmd(), newDriver() (+18 more)

### Community 13 - "Reporter Color & CLI Flags"
Cohesion: 0.09
Nodes (29): newPalette(), ParseColorMode(), ResolveColor(), ColorMode, palette, NewPretty(), equalStrings(), TestJSONSinkPreservesStepLogOrder() (+21 more)

### Community 14 - "Engine DAG & Cluster Events"
Cohesion: 0.09
Nodes (31): fakePipelineBackend (test), engine cluster events tests (retries shape match), dag.Graph.AddEdge, dag.Graph.AddNode, dag.Graph.Descendants, dag.Graph.Levels (topological levels), dag.New, dag.Graph.Nodes (+23 more)

### Community 15 - "Engine Core"
Cohesion: 0.11
Nodes (20): Engine, lookupTaskSpec(), New(), uniqueImages(), upstream(), Options, PipelineInput, taskTimeoutFor() (+12 more)

### Community 16 - "Resolver"
Cohesion: 0.1
Nodes (24): Context, errStepRefDeferred, isArrayStarRef(), lookup(), refPat regex, Substitute(), SubstituteAllowStepRefs(), SubstituteArgs() (+16 more)

### Community 17 - "Backend Interfaces"
Cohesion: 0.13
Nodes (14): Backend, LogSink, PipelineBackend, PipelineRunInvocation, PipelineRunResult, RetryAttempt, RunSpec, StepResult (+6 more)

### Community 18 - "cmdrunner Fakes"
Cohesion: 0.22
Nodes (11): NewFake(), TestFakeRunnerCapturesArgs(), TestDelete(), TestEnsureCreatesIfMissing(), TestEnsureNoopWhenPresent(), TestStatusRunning(), readyControllerDeployment(), readyWebhookDeployment() (+3 more)

### Community 19 - "Doctor"
Cohesion: 0.24
Nodes (11): buildDoctorReport(), checkBinaryOnPath(), checkCacheDir(), checkDocker(), newDoctorCmd(), printDoctorReport(), TestDoctorReportShape(), TestDoctorRequiredForLabels() (+3 more)

### Community 20 - "List & Discovery"
Cohesion: 0.29
Nodes (7): Find(), must(), TestFindsPipelineYAMLAtRoot(), TestFindsTektonDir(), TestNoFilesIsError(), newListCmd(), listResult

### Community 21 - "DAG Tests"
Cohesion: 0.39
Nodes (5): equal(), sameSet(), TestDescendantsOf(), TestLevelsLinear(), TestLevelsParallel()

### Community 22 - "Backend Fake"
Cohesion: 0.33
Nodes (1): fake

### Community 23 - "Tekton Installer"
Cohesion: 0.4
Nodes (2): Installer, Options

### Community 24 - "Docker Integration Tests"
Cohesion: 0.4
Nodes (1): captureLogs

### Community 25 - "Pretty Reporter Output"
Cohesion: 0.4
Nodes (1): prettySink.Emit()

### Community 27 - "PipelineBackend Type Refs"
Cohesion: 0.5
Nodes (4): PipelineBackend optional interface, PipelineRunResult, cluster.Backend, PipelineBackend opt-in interface design

### Community 28 - "Timeout/Failed Status Constants"
Cohesion: 0.5
Nodes (4): Pipeline = 5, Timeout = 6, StatusFailed, StatusTimeout

### Community 29 - "Agent CLI Contract Concepts"
Cohesion: 0.67
Nodes (3): AGENTS.md as canonical doc + embedded in binary, tkn-act doctor preflight diagnostic, tkn-act help-json mechanical introspection

### Community 30 - "Sidecars Limitation Fixture"
Cohesion: 1.0
Nodes (3): sidecars limitation fixture, Pipeline: with-redis, Task: with-redis

### Community 31 - "Step-State Limitation Fixture"
Cohesion: 1.0
Nodes (3): step-state limitation fixture, Pipeline: leaky, Task: leaky

### Community 32 - "Persona B / Sidecars Notes"
Cohesion: 1.0
Nodes (2): Persona B: Tekton-fidelity differentiator, Task.sidecars docker-backend limitation

### Community 33 - "Cluster Lazy Provisioning"
Cohesion: 1.0
Nodes (2): Backend.Prepare (lazy cluster + Tekton install), Lazy cluster + Tekton provisioning on Prepare

### Community 34 - "Stable Agent Contract"
Cohesion: 1.0
Nodes (2): Stable typed exit-code contract, Agent-contract: additive-only changes for v1.2

### Community 35 - "RunTask Unsupported Path"
Cohesion: 1.0
Nodes (2): Backend.RunTask (returns 'not supported'), Backend.RunTask (docker per-step container)

### Community 37 - "Community 37"
Cohesion: 1.0
Nodes (1): Track 1: Tekton-upstream feature parity

### Community 38 - "Community 38"
Cohesion: 1.0
Nodes (1): v1.0 implementation plan

### Community 39 - "Community 39"
Cohesion: 1.0
Nodes (1): Tekton release pinning to v0.65.0

### Community 40 - "Community 40"
Cohesion: 1.0
Nodes (1): Phase 2: per-step results dirs

### Community 41 - "Community 41"
Cohesion: 1.0
Nodes (1): Default unit tests (no build tag)

### Community 42 - "Community 42"
Cohesion: 1.0
Nodes (1): tests-required CI gate

### Community 43 - "Community 43"
Cohesion: 1.0
Nodes (1): PR-only path-scoped workflow gates

### Community 44 - "Community 44"
Cohesion: 1.0
Nodes (1): NewWithClientsAndStores (test constructor)

### Community 45 - "Community 45"
Cohesion: 1.0
Nodes (1): docker.Backend (Backend implementation)

### Community 46 - "Community 46"
Cohesion: 1.0
Nodes (1): engine policy package (timeout + retries)

### Community 47 - "Community 47"
Cohesion: 1.0
Nodes (1): TaskOutcome struct

### Community 48 - "Community 48"
Cohesion: 1.0
Nodes (1): Usage = 2

### Community 49 - "Community 49"
Cohesion: 1.0
Nodes (1): Env = 3

### Community 50 - "Community 50"
Cohesion: 1.0
Nodes (1): Validate = 4

### Community 51 - "Community 51"
Cohesion: 1.0
Nodes (1): Cancelled = 130

### Community 52 - "Community 52"
Cohesion: 1.0
Nodes (1): EvtRunStart

### Community 53 - "Community 53"
Cohesion: 1.0
Nodes (1): EvtRunEnd

### Community 54 - "Community 54"
Cohesion: 1.0
Nodes (1): EvtTaskStart

### Community 55 - "Community 55"
Cohesion: 1.0
Nodes (1): EvtTaskEnd

### Community 56 - "Community 56"
Cohesion: 1.0
Nodes (1): EvtTaskSkip

### Community 57 - "Community 57"
Cohesion: 1.0
Nodes (1): EvtTaskRetry

### Community 58 - "Community 58"
Cohesion: 1.0
Nodes (1): EvtStepStart

### Community 59 - "Community 59"
Cohesion: 1.0
Nodes (1): EvtStepEnd

### Community 60 - "Community 60"
Cohesion: 1.0
Nodes (1): EvtError

### Community 61 - "Community 61"
Cohesion: 1.0
Nodes (1): StatusSucceeded

### Community 62 - "Community 62"
Cohesion: 1.0
Nodes (1): StatusInfraFailed

### Community 63 - "Community 63"
Cohesion: 1.0
Nodes (1): StatusSkipped

### Community 64 - "Community 64"
Cohesion: 1.0
Nodes (1): StatusNotRun

### Community 66 - "Community 66"
Cohesion: 1.0
Nodes (1): Quiet

### Community 67 - "Community 67"
Cohesion: 1.0
Nodes (1): Normal

### Community 68 - "Community 68"
Cohesion: 1.0
Nodes (1): Verbose

### Community 70 - "Community 70"
Cohesion: 1.0
Nodes (1): TestParseColorMode

### Community 71 - "Community 71"
Cohesion: 1.0
Nodes (1): TestResolveColor

### Community 72 - "Community 72"
Cohesion: 1.0
Nodes (1): ParamTypeString

### Community 73 - "Community 73"
Cohesion: 1.0
Nodes (1): ParamTypeArray

### Community 74 - "Community 74"
Cohesion: 1.0
Nodes (1): ParamTypeObject

### Community 75 - "Community 75"
Cohesion: 1.0
Nodes (1): Manager.AllocatedPaths

### Community 76 - "Community 76"
Cohesion: 1.0
Nodes (1): captureSink

## Knowledge Gaps
- **185 isolated node(s):** `clusterStatus`, `doctorCheck`, `doctorReport`, `flagInfo`, `commandInfo` (+180 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **Thin community `Backend Fake`** (6 nodes): `TestFakeImplementsBackend()`, `fake`, `.Cleanup()`, `.Prepare()`, `.RunTask()`, `backend_test.go`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Tekton Installer`** (6 nodes): `install.go`, `New()`, `Installer`, `.Install()`, `.waitReady()`, `Options`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Docker Integration Tests`** (5 nodes): `captureLogs`, `.StepLog()`, `TestRunSingleStep()`, `TestRunStepCapturesResult()`, `docker_integration_test.go`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Pretty Reporter Output`** (5 nodes): `glyph()`, `or()`, `prefixOf()`, `prettySink.Emit()`, `statusWord()`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Persona B / Sidecars Notes`** (2 nodes): `Persona B: Tekton-fidelity differentiator`, `Task.sidecars docker-backend limitation`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Cluster Lazy Provisioning`** (2 nodes): `Backend.Prepare (lazy cluster + Tekton install)`, `Lazy cluster + Tekton provisioning on Prepare`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Stable Agent Contract`** (2 nodes): `Stable typed exit-code contract`, `Agent-contract: additive-only changes for v1.2`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `RunTask Unsupported Path`** (2 nodes): `Backend.RunTask (returns 'not supported')`, `Backend.RunTask (docker per-step container)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 37`** (1 nodes): `Track 1: Tekton-upstream feature parity`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 38`** (1 nodes): `v1.0 implementation plan`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 39`** (1 nodes): `Tekton release pinning to v0.65.0`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 40`** (1 nodes): `Phase 2: per-step results dirs`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 41`** (1 nodes): `Default unit tests (no build tag)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 42`** (1 nodes): `tests-required CI gate`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 43`** (1 nodes): `PR-only path-scoped workflow gates`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 44`** (1 nodes): `NewWithClientsAndStores (test constructor)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 45`** (1 nodes): `docker.Backend (Backend implementation)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 46`** (1 nodes): `engine policy package (timeout + retries)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 47`** (1 nodes): `TaskOutcome struct`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 48`** (1 nodes): `Usage = 2`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 49`** (1 nodes): `Env = 3`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 50`** (1 nodes): `Validate = 4`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 51`** (1 nodes): `Cancelled = 130`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 52`** (1 nodes): `EvtRunStart`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 53`** (1 nodes): `EvtRunEnd`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 54`** (1 nodes): `EvtTaskStart`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 55`** (1 nodes): `EvtTaskEnd`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 56`** (1 nodes): `EvtTaskSkip`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 57`** (1 nodes): `EvtTaskRetry`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 58`** (1 nodes): `EvtStepStart`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 59`** (1 nodes): `EvtStepEnd`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 60`** (1 nodes): `EvtError`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 61`** (1 nodes): `StatusSucceeded`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 62`** (1 nodes): `StatusInfraFailed`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 63`** (1 nodes): `StatusSkipped`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 64`** (1 nodes): `StatusNotRun`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 66`** (1 nodes): `Quiet`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 67`** (1 nodes): `Normal`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 68`** (1 nodes): `Verbose`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 70`** (1 nodes): `TestParseColorMode`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 71`** (1 nodes): `TestResolveColor`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 72`** (1 nodes): `ParamTypeString`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 73`** (1 nodes): `ParamTypeArray`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 74`** (1 nodes): `ParamTypeObject`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 75`** (1 nodes): `Manager.AllocatedPaths`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Community 76`** (1 nodes): `captureSink`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `LoadFiles()` connect `Validate Command & Cluster-Engine Tests` to `Cluster E2E Capture Sink`, `List & Discovery`, `Reporter Color & CLI Flags`?**
  _High betweenness centrality (0.198) - this node is a cross-community bridge._
- **Why does `NewStore()` connect `Cluster Test Helpers & Fakes` to `Cluster E2E Capture Sink`, `Reporter Color & CLI Flags`?**
  _High betweenness centrality (0.188) - this node is a cross-community bridge._
- **Why does `runFixtureDocker()` connect `Cluster E2E Capture Sink` to `Validate Command & Cluster-Engine Tests`, `Cluster Test Helpers & Fakes`?**
  _High betweenness centrality (0.156) - this node is a cross-community bridge._
- **Are the 19 inferred relationships involving `LoadBytes()` (e.g. with `TestEngineLinearOrder()` and `TestEngineFailurePropagation()`) actually correct?**
  _`LoadBytes()` has 19 INFERRED edges - model-reasoned connections that need verification._
- **Are the 16 inferred relationships involving `NewStore()` (e.g. with `buildVolumeStores()` and `TestApplyVolumeSourcesProjectsConfigMap()`) actually correct?**
  _`NewStore()` has 16 INFERRED edges - model-reasoned connections that need verification._
- **Are the 13 inferred relationships involving `Substitute()` (e.g. with `substituteStepRefs()` and `.runOne()`) actually correct?**
  _`Substitute()` has 13 INFERRED edges - model-reasoned connections that need verification._
- **Are the 13 inferred relationships involving `newRootCmd()` (e.g. with `TestHelpJSONShape()` and `TestHelpJSONDoesNotIncludeRoot()`) actually correct?**
  _`newRootCmd()` has 13 INFERRED edges - model-reasoned connections that need verification._