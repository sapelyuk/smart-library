# =============================================================================
# eval-rag.ps1 - quality gate for the Book RAG workflow
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\eval-rag.ps1 -ValidateOnly
#   powershell -ExecutionPolicy Bypass -File .\scripts\eval-rag.ps1
#   powershell -ExecutionPolicy Bypass -File .\scripts\eval-rag.ps1 -Mode agent -K 5
#
# WHY THIS EXISTS
#   `validate_workflow.py` proves the workflow JSON is structurally sound, and
#   the smoke tests prove the endpoints answer. Neither answers the only question
#   that matters to a user: *are the recommendations any good?* This script runs
#   a fixed set of questions with known-good answers and reports precision@k and
#   recall@k against them, so a change that quietly degrades retrieval fails a
#   test instead of silently shipping.
#
# WHAT IT MEASURES
#   retrieve (default) - embeds each question with Gemini, ranks the library with
#       the same cosine distance the workflow's vector store uses, and scores the
#       top-k DISTINCT books. One embedding call per question, no chat model, so
#       it is cheap, deterministic, and runnable while the chat quota is empty.
#   agent - POSTs each question to the real /webhook/book-rag/recommend endpoint
#       and scores the books the agent actually named. End to end, but it spends
#       several Gemini requests per question and inherits chat-memory state.
#
#   Metrics, for the top-k of a question (see evals/README.md for the exact
#   definitions):
#     precision@k = expected books in top-k / books returned (DUPLICATES REMOVED,
#                   because a recommender that repeats one book is not precise)
#     recall@k    = expected books in top-k / expected books
#     hit@k       = 1 when at least one expected book was retrieved, else 0
#     MRR         = 1 / rank of the first expected book
#
# REQUIREMENTS
#   retrieve : docker container n8n-book-rag-db up + GEMINI_API_KEY in .env
#   agent    : the above plus n8n running, workflow Active, 3 credentials attached
#
# NOTHING IS MODIFIED. The script only reads: it queries the catalogue, asks for
# embeddings, and (in agent mode) sends questions. It writes exactly one file,
# the report, under evals/results/. It never ingests, updates or deletes.
#
# EXIT CODES
#   0 = gates passed (or -ValidateOnly found no problems)
#   1 = a gate failed, an unexpected error occurred, or validation failed
#   2 = bad invocation (missing suite file, wrong parameters)
# =============================================================================

[CmdletBinding(DefaultParameterSetName = 'Run')]
param(
    # The evaluation suite: questions plus the books that should answer them.
    [string] $Suite = 'evals\rag-eval-suite.json',

    # retrieve = rank the library locally with embeddings; agent = call the real
    # webhook and score what the agent named; both = run retrieve then agent.
    [Parameter(ParameterSetName = 'Run')]
    [ValidateSet('retrieve', 'agent', 'both')]
    [string] $Mode = 'retrieve',

    # Top-k. Overrides the suite's `defaults.k` when the suite does not set its own.
    [Parameter(ParameterSetName = 'Run')]
    [int] $K = 0,

    # Offline: validate the suite and report what WOULD run. No network, no docker.
    [Parameter(ParameterSetName = 'Validate')]
    [switch] $ValidateOnly,

    # Gates. A question fails when a metric is below its minimum; the run fails
    # when any question fails. 0 disables a gate.
    [Parameter(ParameterSetName = 'Run')]
    [double] $MinPrecision = 0.40,
    [Parameter(ParameterSetName = 'Run')]
    [double] $MinSelfRecall = 1.00,
    [Parameter(ParameterSetName = 'Run')]
    [double] $MinRecall = 0.60,
    [Parameter(ParameterSetName = 'Run')]
    [double] $MinHitRate = 0.60,
    [Parameter(ParameterSetName = 'Run')]
    [double] $MinRefusePrecision = 1.00,
    [Parameter(ParameterSetName = 'Run')]
    [int] $MaxErrors = 0,

    # Where the .json/.md report and baseline live. 0 = do not write a report.
    [Parameter(ParameterSetName = 'Run')]
    [string] $OutDir = 'evals\results',
    [Parameter(ParameterSetName = 'Run')]
    [string] $Baseline = 'evals\baseline.json',
    # Allowed drop against the baseline before the run is called a regression.
    [Parameter(ParameterSetName = 'Run')]
    [double] $MaxRegression = 0.05,

    # Seconds to wait for one embedding / one agent answer.
    [Parameter(ParameterSetName = 'Run')]
    [int] $TimeoutSec = 180,

    # Replay a saved results JSON instead of asking anything. No docker, no
    # embeddings, no n8n: this re-runs the gates, the aggregate and the report over
    # a previous run, so thresholds can be re-checked without spending quota, and
    # the report itself can be tested offline. The fixture files in evals/fixtures
    # are the intended input.
    [Parameter(ParameterSetName = 'Run')]
    [string] $InputResults,

    # After a PASSING live run, save it to $Baseline so later runs are compared to
    # it. Promotion is refused for a failing run or for a replay.
    [Parameter(ParameterSetName = 'Run')]
    [switch] $PromoteBaseline
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root

# -----------------------------------------------------------------------------
# Output helpers (same shape as the other scripts in this repo)
# -----------------------------------------------------------------------------
function Section($t) { Write-Host "`n=== $t ===" -ForegroundColor Cyan }
function Ok($t)      { Write-Host "  OK   $t" -ForegroundColor Green }
function Warn($t)    { Write-Host "  WARN $t" -ForegroundColor Yellow }
function Bad($t)     { Write-Host "  FAIL $t" -ForegroundColor Red }
function Info($t)    { Write-Host "  ..   $t" -ForegroundColor Gray }

$script:Warnings = @()

# JSON always writes a dot decimal separator. This machine's culture does not (it
# uses a comma), and [double]::TryParse follows the culture - so parsing "0.67"
# would silently FAIL and yield 0. Every numeric parse below is therefore done with
# the invariant culture. This bit the aggregate once already; do not "simplify" it.
$script:InvariantCulture = [System.Globalization.CultureInfo]::InvariantCulture

# =============================================================================
# Small utilities
# =============================================================================

function Get-EnvMap {
    <# Reads .env into a hashtable. Secrets stay in memory; only presence is logged. #>
    $map = @{}
    if (Test-Path '.env') {
        foreach ($line in (Get-Content '.env')) {
            $m = [regex]::Match($line, '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$')
            if ($m.Success) { $map[$m.Groups[1].Value] = $m.Groups[2].Value.Trim() }
        }
    }
    return $map
}

function Get-JsonFile {
    param([string] $Path)
    return (Get-Content -Path $Path -Raw -Encoding UTF8 | ConvertFrom-Json)
}

function New-Lookup {
    <# Case-insensitive hashtable. PS 5.1's ConvertFrom-Json gives PSCustomObject
       for objects, so plain @{} + [string] keys is the portable choice. #>
    return New-Object 'System.Collections.Hashtable' ([System.StringComparer]::OrdinalIgnoreCase)
}

function Get-Prop {
    <# Reads a property that may be absent (null) without throwing. #>
    param($Object, [string] $Name, $Default = $null)
    if ($null -eq $Object) { return $Default }
    if ($Object.PSObject.Properties.Name -contains $Name) {
        $v = $Object.$Name
        if ($null -eq $v) { return $Default }
        return $v
    }
    return $Default
}

function Get-DisplayTitle {
    <# "Sapiens: A Brief History of Humankind" -> "Sapiens"; keeps titles short in tables. #>
    param([string] $Title)
    if ([string]::IsNullOrWhiteSpace($Title)) { return $Title }
    $t = $Title
    $i = $t.IndexOf(':')
    if ($i -gt 0) { $t = $t.Substring(0, $i) }
    return $t.Trim()
}

function ConvertTo-Number {
    <# PowerShell 5.1 has no [double]::TryParse-friendly coercion for mixed JSON,
       and the culture-aware parse would fail on a dot decimal separator. #>
    param($Value, [double] $Default = 0.0)
    if ($null -eq $Value) { return $Default }
    $d = 0.0
    if ([double]::TryParse(([string]$Value), [System.Globalization.NumberStyles]::Float, $script:InvariantCulture, [ref]$d)) { return $d }
    return $Default
}

function Round2 {
    param([double] $Value)
    return [math]::Round($Value, 2)
}

function Show-Num {
    <# Formats a metric for a report. ALWAYS with a dot decimal separator: the
       machine's own culture writes 0.74 as "0,74", which in a technical report
       reads as a different number. $null (undefined for a 'refuse' question) is
       shown as '-' rather than as a misleading 0. #>
    param($Value)
    if ($null -eq $Value) { return '-' }
    $d = 0.0
    if (-not [double]::TryParse(([string]$Value), [System.Globalization.NumberStyles]::Float, $script:InvariantCulture, [ref]$d)) { return [string]$Value }
    return $d.ToString('0.##', $script:InvariantCulture)
}

function Join-Ids {
    <# Renders ["a","b"] as "a, b". #>
    param($Ids)
    if ($null -eq $Ids) { return '' }
    return (($Ids | ForEach-Object { [string]$_ }) -join ', ')
}

# =============================================================================
# Scoring - the metric definitions live here and NOWHERE else
# =============================================================================

function Get-MetricSet {
    <#
      Scores one question.

      $RankedIds  distinct books in ranked order (best first), already truncated
                  to k by the caller. Duplicates are removed BEFORE scoring, so
                  precision@k reflects a recommender that does not repeat itself.
      $Expected   the books that answer the question. Empty means "refuse": the
                  correct behaviour is to name nothing at all.

      Returns precision, recall, f1, hit, reciprocal rank, and rank@1 of the first
      expected book.
    #>
    param($RankedIds, $Expected)

    # Duplicates are removed BEFORE scoring, so a recommender that repeats one book
    # is not rewarded for it. Mirrors score() in scripts/eval_metrics.py.
    $ranked = @()
    $seenRanked = @{}
    foreach ($id in @($RankedIds)) {
        if ($seenRanked.ContainsKey($id)) { continue }
        $seenRanked[$id] = $true
        $ranked += $id
    }
    $expected = @($Expected)

    $rel = @{}
    foreach ($e in $expected) { $rel[$e] = $true }

    $hits = @($ranked | Where-Object { $rel.ContainsKey($_) })
    $tp = $hits.Count
    $nRanked = $ranked.Count
    $nExpected = $expected.Count

    $precision = if ($nRanked -gt 0)  { $tp / [double]$nRanked }   else { 0.0 }
    $recall    = if ($nExpected -gt 0) { $tp / [double]$nExpected } else { $null }

    # A "refuse" question has no expected book: precision is the whole story (the
    # correct answer names nothing), recall is meaningless, and hit/MRR are not
    # defined either. They are $null and are excluded from the averages rather than
    # counted as failures. Mirrors score() in scripts/eval_metrics.py.
    $hit = $null
    $reciprocal = $null
    if ($nExpected -eq 0) {
        $precision = if ($nRanked -eq 0) { 1.0 } else { 0.0 }
    }
    else {
        $hit = if ($tp -gt 0) { 1 } else { 0 }
    }

    $firstRank = 0
    for ($i = 0; $i -lt $nRanked; $i++) {
        if ($rel.ContainsKey($ranked[$i])) { $firstRank = $i + 1; break }
    }
    if ($nExpected -gt 0) { $reciprocal = if ($firstRank -gt 0) { 1.0 / $firstRank } else { 0.0 } }

    $f1 = $null
    if ($null -ne $recall) {
        $f1 = if (($precision + $recall) -gt 0) { 2 * $precision * $recall / ($precision + $recall) } else { 0.0 }
    }

    return [pscustomobject]@{
        precision        = Round2 $precision
        recall           = if ($null -eq $recall) { $null } else { Round2 $recall }
        f1               = if ($null -eq $f1) { $null } else { Round2 $f1 }
        hit              = $hit
        mrr              = if ($null -eq $reciprocal) { $null } else { Round2 $reciprocal }
        rank_first_hit   = $firstRank
        true_positives   = $tp
        returned         = $nRanked
        expected_count   = $nExpected
        relevant_ranked  = @($hits)
        missing          = @($expected | Where-Object { -not ($ranked -contains $_) })
    }
}

function Get-Aggregate {
    <# Macro-averages a list of per-question metric sets (every question counts once).

       Undefined values ($null - a 'refuse' question has no hit, MRR, F1 or recall)
       are EXCLUDED from the average and its denominator, so a refusal check neither
       inflates nor deflates the retrieval metrics. Mirrors aggregate() in
       scripts/eval_metrics.py. #>
    param($Results)
    $items = @($Results)
    if ($items.Count -eq 0) {
        return [pscustomobject]@{ questions = 0; precision = 0; recall = 0; f1 = 0; hit_rate = 0; mrr = 0; passed = 0; failed = 0 }
    }

    $sums = @{}
    $counts = @{}
    foreach ($metric in 'precision', 'recall', 'f1', 'hit_rate', 'mrr') {
        $sums[$metric] = 0.0
        $counts[$metric] = 0
    }
    $passed = 0; $failed = 0

    foreach ($it in $items) {
        $values = @{
            precision = $it.metrics.precision
            recall    = $it.metrics.recall
            f1        = $it.metrics.f1
            # per-question rows carry `hit`; the aggregate reports it as hit_rate
            hit_rate  = $it.metrics.hit
            mrr       = $it.metrics.mrr
        }
        foreach ($metric in $values.Keys) {
            if ($null -eq $values[$metric]) { continue }
            $sums[$metric] += (ConvertTo-Number $values[$metric])
            $counts[$metric]++
        }
        if ($it.passed) { $passed++ } else { $failed++ }
    }

    $mean = @{}
    foreach ($metric in $sums.Keys) {
        # No defined values means UNDEFINED, not zero: a category holding only a
        # 'refuse' question has no hit rate or MRR, and reporting 0 there would be a
        # fabricated number. It is reported as '-' instead.
        $mean[$metric] = if ($counts[$metric] -gt 0) { Round2 ($sums[$metric] / [double]$counts[$metric]) } else { $null }
    }

    return [pscustomobject]@{
        questions = $items.Count
        precision = $mean['precision']
        recall    = $mean['recall']
        f1        = $mean['f1']
        hit_rate  = $mean['hit_rate']
        mrr       = $mean['mrr']
        passed    = $passed
        failed    = $failed
    }
}

# =============================================================================
# Title matching (agent mode only)
# =============================================================================

function Get-NormalisedTitle {
    <#
      Lowercase, punctuation -> space, collapse spaces, drop a leading article.
      "Thinking, Fast and Slow" -> "thinking fast and slow"
      "The City and the City"   -> "city and the city"
      Deliberately does NOT touch inner articles: "The Name of the Wind" keeps
      its "of"/"the", so a match still means the real title was used.
    #>
    param([string] $Title)
    if ([string]::IsNullOrWhiteSpace($Title)) { return '' }
    $t = $Title.ToLowerInvariant()
    $t = [regex]::Replace($t, '[^a-z0-9]+', ' ')
    $t = [regex]::Replace($t, '\s+', ' ').Trim()
    $t = [regex]::Replace($t, '^(the|a|an) ', '')
    return $t.Trim()
}

function Get-TitleMapFromCatalogue {
    <#
      Book id -> normalised title. Titles that share a normalised form collapse
      into one key; the caller warns about that, because it makes scoring
      ambiguous rather than wrong.
      Longest title first so a substring never shadows a full title.
    #>
    param($Catalogue)
    $map = New-Lookup
    foreach ($b in @($Catalogue)) {
        $norm = Get-NormalisedTitle $b.title
        if (-not $norm) { continue }
        if (-not $map.ContainsKey($norm)) { $map[$norm] = @() }
        $map[$norm] = @($map[$norm]) + $b.book_id
    }
    return $map
}

function Get-BookIdsMentioned {
    <#
      The books whose titles appear in a blob of text, in the order the text first
      mentions them. Mirrors mentioned_books() in scripts/eval_metrics.py; every
      rule below is load-bearing and pinned by tests/test_eval_metrics.py:

        * longest title first, so a shorter title cannot shadow a longer one
        * a full title must appear complete: every one of its words, in order
        * a TRUNCATED title is accepted as a prefix - "Sapiens is the obvious
          pick" matches "Sapiens: A Brief History of Humankind", because an
          answer rarely repeats a subtitle and demanding it would miss a real
          recommendation
        * KNOWN COST of that rule, accepted deliberately: the words of a
          truncated title need not be adjacent, so "The quiet city slept" also
          matches "The City and the City". Matching the full title always works;
          the cost is only that a partial title is loose. It errs toward
          crediting a book the agent really did name, and the questions where a
          false positive would matter ask for whole titles.

      Titles named in the QUESTION are removed by the caller, not here, so this
      function stays a pure text scan.
    #>
    param([string] $Text, $TitleMap, $Catalogue)

    if ([string]::IsNullOrWhiteSpace($Text) -or -not $TitleMap) { return @() }

    $flat = [regex]::Replace($Text.ToLowerInvariant(), '[^a-z0-9]+', ' ')
    $flat = ' ' + ([regex]::Replace($flat, '\s+', ' ').Trim()) + ' '

    $positions = @{}
    foreach ($key in @($TitleMap.Keys | Sort-Object -Property Length -Descending)) {
        if ($key.Length -lt 3) { continue }
        # The title's words in order, each later word optional (a truncated title),
        # then one trailing word boundary.
        #   sapiens(?: a)?(?: brief)?(?: history)?(?: of)?(?: humankind)?(?![a-z0-9])
        # matches "Sapiens is the obvious pick" and the full subtitle form, while
        # "The quiet city slept" does NOT match "the city and the city" because a
        # skipped word leaves the next one unmatched.
        $words = @($key -split ' ' | ForEach-Object { [regex]::Escape($_) })
        $pattern = $words[0]
        for ($w = 1; $w -lt $words.Count; $w++) { $pattern += '(?:' + $words[$w] + ')?' }
        $pattern += '(?![a-z0-9])'
        $m = [regex]::Match($flat, $pattern)
        if (-not $m.Success) { continue }
        foreach ($id in @($TitleMap[$key])) {
            if (-not $positions.ContainsKey($id)) { $positions[$id] = $m.Index }
        }
    }
    return @($positions.GetEnumerator() | Sort-Object -Property Value | ForEach-Object { $_.Key })
}

# =============================================================================
# Data access
# =============================================================================

function Get-DbSettings {
    param($Env)
    $u = if ($Env['POSTGRES_USER']) { $Env['POSTGRES_USER'] } else { 'bookrag' }
    $d = if ($Env['POSTGRES_DB'])   { $Env['POSTGRES_DB'] }   else { 'bookrag' }
    return [pscustomobject]@{ user = $u; db = $d }
}

function Invoke-Sql {
    <# Same access path as verify-db.ps1: psql inside the container, tuples only. #>
    param([string] $Sql, $Db)
    $out = docker exec n8n-book-rag-db psql -U $Db.user -d $Db.db -t -A -F '|' -c $Sql 2>&1
    if ($LASTEXITCODE -ne 0) { throw "psql failed: $out" }
    return @($out)
}

function Test-DbUp {
    param($Db)
    # 2>$null: when docker is installed but this shell cannot reach its pipe (a
    # sandbox, or Docker Desktop not running) docker prints to stderr, and that must
    # not look like a crash. The state check and the query below are the real test.
    $state = docker inspect --format '{{.State.Status}}' n8n-book-rag-db 2>$null
    if ($state -ne 'running') { return $false }
    try { $null = Invoke-Sql 'select 1;' $Db; return $true } catch { return $false }
}

function Get-DbCatalogue {
    <# book_id -> title from the live database. This is what the answers must come from. #>
    param($Db)
    $rows = Invoke-Sql "select book_id || '|' || coalesce(title,'') || '|' || coalesce(genre,'') from books order by title;" $Db
    $list = @()
    foreach ($row in $rows) {
        if ([string]::IsNullOrWhiteSpace($row)) { continue }
        $parts = $row -split '\|'
        if ($parts.Count -lt 2) { continue }
        $list += [pscustomobject]@{ book_id = $parts[0].Trim(); title = $parts[1].Trim(); genre = if ($parts.Count -gt 2) { $parts[2].Trim() } else { '' } }
    }
    return $list
}

function Get-FileCatalogue {
    <# Offline fallback for -ValidateOnly: the suite is checked against the sample CSV. #>
    param([string] $Path = 'samples\books.csv')
    $list = @()
    if (-not (Test-Path $Path)) { return $list }
    foreach ($row in (Import-Csv -Path $Path)) {
        $list += [pscustomobject]@{ book_id = $row.book_id.Trim(); title = $row.title.Trim(); genre = $row.genre.Trim() }
    }
    return $list
}

function Invoke-GeminiEmbedding {
    <#
      One embedding call. Returns @(doubles). Throws with the API's own message
      on failure, because "429 quota" and "400 bad model" need different fixes.
    #>
    param([string] $Text, [string] $ApiKey, [string] $Model, [int] $TimeoutSec)

    $uri = "https://generativelanguage.googleapis.com/v1beta/$Model`:embedContent"
    $payload = @{
        model   = $Model
        content = @{ parts = @(@{ text = $Text }) }
    }
    $json = $payload | ConvertTo-Json -Depth 8 -Compress

    try {
        $resp = Invoke-RestMethod -Method Post -Uri $uri `
            -Headers @{ 'x-goog-api-key' = $ApiKey } `
            -ContentType 'application/json; charset=utf-8' `
            -Body ([System.Text.Encoding]::UTF8.GetBytes($json)) `
            -TimeoutSec $TimeoutSec
    }
    catch {
        $status = ''
        if ($_.Exception.Response) { $status = "HTTP $([int]$_.Exception.Response.StatusCode)" }
        throw "embedding request failed ($status): $($_.Exception.Message)"
    }

    if (-not $resp.embedding -or -not $resp.embedding.values) { throw "embedding response contained no values" }
    return @($resp.embedding.values)
}

function Get-RetrievedBooks {
    <#
      The retrieval half of the eval, expressed in the SAME terms the workflow
      uses: cosine distance over halfvec, DISTINCT ON the book so one book cannot
      occupy several top-k slots, ordered exactly like match_book_chunks.
    #>
    param($Embedding, [int] $K, $Db)

    $vec = '[' + ((@($Embedding) | ForEach-Object { [string]$_ }) -join ',') + ']'
    $sql = @"
select book_id, title, similarity from (
  select distinct on (coalesce(metadata->>'book_id', 'chunk:' || id::text))
         coalesce(metadata->>'book_id', 'chunk:' || id::text) as book_id,
         coalesce(b.title, metadata->>'title', '') as title,
         1 - (c.embedding <=> '$vec'::halfvec) as similarity
    from book_chunks c
    left join books b on b.book_id = c.metadata->>'book_id'
   where c.embedding is not null
   order by coalesce(metadata->>'book_id', 'chunk:' || id::text), c.embedding <=> '$vec'::halfvec
) t
order by similarity desc
limit $K;
"@

    $rows = Invoke-Sql $sql $Db
    $ranked = @()
    foreach ($row in $rows) {
        if ([string]::IsNullOrWhiteSpace($row)) { continue }
        $parts = $row -split '\|'
        if ($parts.Count -lt 3) { continue }
        $ranked += [pscustomobject]@{
            id         = $parts[0].Trim()
            title      = Get-DisplayTitle $parts[1].Trim()
            similarity = Round2 (ConvertTo-Number $parts[2])
        }
    }
    return $ranked
}

function Invoke-AgentRecommend {
    <# One end-to-end call through the real webhook. Returns the answer text. #>
    param([string] $Question, [string] $SessionId, [string] $BaseUrl, $Headers, [int] $TimeoutSec)

    $body = @{ chatInput = $Question; sessionId = $SessionId } | ConvertTo-Json -Depth 4 -Compress
    try {
        $resp = Invoke-RestMethod -Method Post -Uri "$BaseUrl/webhook/book-rag/recommend" `
            -Headers $Headers -ContentType 'application/json; charset=utf-8' `
            -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec $TimeoutSec
    }
    catch {
        $status = ''
        if ($_.Exception.Response) { $status = "HTTP $([int]$_.Exception.Response.StatusCode)" }
        throw "recommend webhook failed ($status): $($_.Exception.Message)"
    }
    if ($resp -is [string]) { return $resp }
    $out = Get-Prop $resp 'output' ''
    return [string]$out
}

# =============================================================================
# Suite validation - the offline half, and the reason this script can be trusted
# =============================================================================

$script:ValidCategories = @('self', 'taste', 'genre', 'author', 'constraint', 'refuse')

function Test-Suite {
    <#
      Returns @{ errors = @(); warnings = @(); questions = @() }.

      Every error here is something that would silently produce a MEANINGLESS
      score later: an expected id that no longer exists in the catalogue (the
      question would look like a permanent failure), a duplicate question id (the
      report could not tell two rows apart), a refuse question with expected
      books (the metric would contradict the question's intent).
    #>
    param($SuiteData, $Catalogue, [int] $KOverride)

    $errors = @()
    $warnings = @()
    $questions = @()

    if (-not $SuiteData)                               { $errors += "suite file is empty"; }
    if (-not (Get-Prop $SuiteData 'suite' ''))         { $warnings += "suite has no 'suite' name" }
    if (-not (Get-Prop $SuiteData 'questions'))        { $errors += "suite has no 'questions' array"; return @{ errors = $errors; warnings = $warnings; questions = $questions } }

    $catalogueIds = @{}
    $catalogueTitles = @{}
    foreach ($b in @($Catalogue)) {
        $catalogueIds[$b.book_id] = $true
        $catalogueTitles[$b.book_id] = $b.title
    }
    if ($catalogueIds.Count -eq 0) { $warnings += "catalogue is empty - cannot check that expected ids exist" }

    $defaults = Get-Prop $SuiteData 'defaults' $null
    $defaultK = [int](ConvertTo-Number (Get-Prop $defaults 'k' 5) 5)
    if ($KOverride -gt 0) { $defaultK = $KOverride }
    if ($defaultK -lt 1) { $errors += "effective k is $defaultK; it must be >= 1" }

    $seenIds = @{}
    $used = @{}
    $index = 0

    foreach ($q in @($SuiteData.questions)) {
        $index++
        $id = [string](Get-Prop $q 'id' '')
        $label = if ($id) { $id } else { "question #$index" }

        if (-not $id) { $errors += "question #$index has no 'id'" }
        elseif ($id -notmatch '^[a-z0-9][a-z0-9\-]*$') { $errors += "$label : id must be lowercase letters, digits and dashes" }
        elseif ($seenIds.ContainsKey($id)) { $errors += "$label : duplicate id" }
        else { $seenIds[$id] = $true }

        $text = [string](Get-Prop $q 'question' '')
        if ([string]::IsNullOrWhiteSpace($text)) { $errors += "$label : 'question' is empty" }

        $category = [string](Get-Prop $q 'category' (Get-Prop $defaults 'category' 'taste'))
        if ($script:ValidCategories -notcontains $category) {
            $errors += "$label : unknown category '$category' (allowed: $($script:ValidCategories -join ', '))"
        }

        $expected = @()
        $rawExpected = Get-Prop $q 'expected' @()
        if ($null -ne $rawExpected) { $expected = @($rawExpected | ForEach-Object { [string]$_ } | Where-Object { $_ -ne '' }) }

        $also = @()
        $rawAlso = Get-Prop $q 'also_mentions' @()
        if ($null -ne $rawAlso) { $also = @($rawAlso | ForEach-Object { [string]$_ } | Where-Object { $_ -ne '' }) }

        $qk = [int](ConvertTo-Number (Get-Prop $q 'k' $defaultK) $defaultK)
        if ($qk -lt 1) { $errors += "$label : k must be >= 1" }

        if ($expected.Count -eq 0 -and $category -ne 'refuse') {
            $errors += "$label : category '$category' needs at least one expected book (use category 'refuse' for questions where nothing should match)"
        }
        if ($expected.Count -gt 0 -and $category -eq 'refuse') {
            $errors += "$label : category 'refuse' must have an empty 'expected' list - it means 'name nothing'"
        }
        if ($category -eq 'self' -and $expected.Count -ne 1) {
            $errors += "$label : category 'self' must have exactly one expected book"
        }
        if ($expected.Count -gt $qk) {
            $warnings += "$label : $($expected.Count) expected books but k=$qk, so recall@k can never exceed $(Round2 ($qk / [double]$expected.Count))"
        }

        foreach ($list in @(@{ name = 'expected'; ids = $expected }, @{ name = 'also_mentions'; ids = $also })) {
            $dupCheck = @{}
            foreach ($eid in $list.ids) {
                if ($dupCheck.ContainsKey($eid)) { $errors += "$label : '$eid' appears twice in $($list.name)" }
                $dupCheck[$eid] = $true
                if ($catalogueIds.Count -gt 0 -and -not $catalogueIds.ContainsKey($eid)) {
                    $errors += "$label : $($list.name) id '$eid' is not in the catalogue ($($catalogueIds.Count) books)"
                } elseif ($catalogueIds.Count -gt 0) {
                    $used[$eid] = $true
                }
            }
        }
        foreach ($eid in $expected) {
            if ($also -contains $eid) { $errors += "$label : '$eid' is both expected and also_mentions" }
        }

        # A question that names an expected book in its own text leaks the answer:
        # in agent mode the echo would be scored as a hit. Category 'self' is
        # exempt BY DESIGN - naming the book is the whole test - so the leak check
        # only applies elsewhere, and must still be declared explicitly there.
        if ($category -ne 'self') {
            $named = @{}
            foreach ($eid in $expected) {
                if ($catalogueTitles.ContainsKey($eid)) {
                    $norm = Get-NormalisedTitle $catalogueTitles[$eid]
                    if ($norm -and ([regex]::Replace($text.ToLowerInvariant(), '[^a-z0-9]+', ' ') -match ([regex]::Escape($norm) + '(?![a-z0-9])'))) { $named[$eid] = $true }
                }
            }
            foreach ($eid in $named.Keys) {
                if ($also -notcontains $eid) {
                    $warnings += "$label : the question text names expected book '$eid' but it is not in 'also_mentions', so an echo of the question counts as a hit"
                }
            }
        }

        $questions += [pscustomobject]@{
            index          = $index
            id             = $id
            category       = $category
            question       = $text
            expected_ids   = $expected
            also_ids       = $also
            k              = $qk
            note           = [string](Get-Prop $q 'note' '')
        }
    }

    if ($questions.Count -eq 0) { $errors += "suite contains no questions" }
    if ($catalogueIds.Count -gt 0) {
        $uncovered = @($catalogueIds.Keys | Where-Object { -not $used.ContainsKey($_) })
        if ($uncovered.Count -gt 0) {
            $warnings += "$($uncovered.Count) of $($catalogueIds.Count) catalogue books never appear as an expected answer: $(Join-Ids ($uncovered | Sort-Object))"
        }
    }
    foreach ($category in $script:ValidCategories) {
        if (-not (@($questions | Where-Object { $_.category -eq $category }).Count)) {
            $warnings += "no questions in category '$category'"
        }
    }

    return @{ errors = $errors; warnings = $warnings; questions = $questions; defaultK = $defaultK }
}

function Show-Validation {
    param($Validation, $Catalogue, [int] $DefaultK, [string] $SuitePath)

    $questions = @($Validation.questions)
    Section "Suite"
    Info "file      : $SuitePath"
    Info "catalogue : $($Catalogue.Count) book(s)"
    Info "questions : $($questions.Count) (default k = $DefaultK)"
    if ($questions.Count) {
        $byCategory = $questions | Group-Object category | Sort-Object Name
        Info "categories: $(($byCategory | ForEach-Object { "$($_.Name) $($_.Count)" }) -join ', ')"
    }
    $avgExpected = if ($questions.Count) { Round2 ((($questions | ForEach-Object { $_.expected_ids.Count }) | Measure-Object -Sum).Sum / [double]$questions.Count) } else { 0 }
    Info "avg expected books per question: $avgExpected"

    if ($Validation.warnings.Count) {
        Section "Warnings ($($Validation.warnings.Count))"
        foreach ($w in $Validation.warnings) { Warn $w }
    }
    if ($Validation.errors.Count) {
        Section "Errors ($($Validation.errors.Count))"
        foreach ($e in $Validation.errors) { Bad $e }
    }
}

# =============================================================================
# Reporting
# =============================================================================

function New-MarkdownReport {
    param($Run, [string] $Path)

    $sb = New-Object System.Text.StringBuilder
    $null = $sb.AppendLine("# RAG evaluation - $($Run.mode) mode")
    $null = $sb.AppendLine()
    $null = $sb.AppendLine("| | |")
    $null = $sb.AppendLine("|---|---|")
    $null = $sb.AppendLine("| Run at | $($Run.started_at) |")
    if ($Run.PSObject.Properties.Name -contains 'replayed_from' -and $Run.replayed_from) {
        $null = $sb.AppendLine("| Replayed at | $($Run.replayed_at) |")
        $null = $sb.AppendLine("| Replayed from | ``$($Run.replayed_from)`` |")
    }
    $null = $sb.AppendLine("| Suite | ``$($Run.suite)`` v$($Run.suite_version) |")
    $null = $sb.AppendLine("| Mode | $($Run.mode) |")
    $null = $sb.AppendLine("| k | $($Run.k) |")
    $null = $sb.AppendLine("| Questions | $($Run.totals.questions) |")
    $null = $sb.AppendLine("| Catalogue | $($Run.catalogue_count) book(s) |")
    $null = $sb.AppendLine("| Verdict | **$(if ($Run.verdict -eq 'PASS') { 'PASS' } else { 'FAIL' })** |")
    $null = $sb.AppendLine()
    $null = $sb.AppendLine("## Aggregate")
    $null = $sb.AppendLine()
    $null = $sb.AppendLine("| Metric | Overall | self | taste | genre | author | constraint | refuse |")
    $null = $sb.AppendLine("|---|---|---|---|---|---|---|---|")

    $cats = @('self', 'taste', 'genre', 'author', 'constraint', 'refuse')
    $catProps = @()
    if ($null -ne $Run.by_category) { $catProps = @($Run.by_category.PSObject.Properties.Name) }
    foreach ($metric in @('precision', 'recall', 'f1', 'hit_rate', 'mrr')) {
        $cells = @()
        foreach ($source in @($null) + $cats) {
            $value = $null
            if ($null -eq $source) { $value = $Run.aggregate.$metric }
            elseif ($catProps -contains $source) { $value = $Run.by_category.$source.$metric }
            $cells += (Show-Num $value)
        }
        $null = $sb.AppendLine("| $metric | $($cells -join ' | ') |")
    }
    $questionCells = @($cats | ForEach-Object {
        if ($catProps -contains $_) { "$($Run.by_category.$_.questions)" } else { '-' }
    })
    $null = $sb.AppendLine("| questions | $($Run.aggregate.questions) | $($questionCells -join ' | ') |")
    $null = $sb.AppendLine()
    $null = $sb.AppendLine("## Gates")
    $null = $sb.AppendLine()
    $null = $sb.AppendLine("| Gate | Threshold | Actual | Result |")
    $null = $sb.AppendLine("|---|---|---|---|")
    foreach ($g in $Run.gates) {
        $null = $sb.AppendLine("| $($g.name) | $($g.threshold) | $($g.actual) | $(if ($g.ok) { 'ok' } else { '**FAIL**' }) |")
    }
    $null = $sb.AppendLine()
    $null = $sb.AppendLine("## Per question")
    $null = $sb.AppendLine()
    $null = $sb.AppendLine("| id | category | prec | rec | hit | returned | expected found | missing | first hit |")
    $null = $sb.AppendLine("|---|---|---|---|---|---|---|---|---|")
    foreach ($q in $Run.questions) {
        if ($q.error) {
            $null = $sb.AppendLine("| $($q.id) | $($q.category) | - | - | - | - | - | - | error: $($q.error) |")
            continue
        }
        $null = $sb.AppendLine("| $($q.id) | $($q.category) | $(Show-Num $q.metrics.precision) | $(Show-Num $q.metrics.recall) | $(Show-Num $q.metrics.hit) | $($q.metrics.returned) | $(Join-Ids $q.returned_titles) | $(Join-Ids $q.missing_titles) | $($q.metrics.rank_first_hit) |")
    }
    $null = $sb.AppendLine()
    $null = $sb.AppendLine("## Answers returned")
    $null = $sb.AppendLine()
    foreach ($q in $Run.questions) {
        $null = $sb.AppendLine("### $($q.id) - $($q.question)")
        $null = $sb.AppendLine()
        if ($q.expected_titles.Count) { $null = $sb.AppendLine("Expected: $(Join-Ids $q.expected_titles)") } else { $null = $sb.AppendLine("Expected: *nothing - this is a refusal check*") }
        if ($q.error) {
            $null = $sb.AppendLine()
            $null = $sb.AppendLine("**ERROR:** $($q.error)")
        }
        elseif (@($q.returned_titles).Count -eq 0) {
            $null = $sb.AppendLine()
            $null = $sb.AppendLine("Returned: *nothing*")
        }
        else {
            $null = $sb.AppendLine()
            $null = $sb.AppendLine("Returned, best first: $(Join-Ids $q.returned_titles)")
        }
        if ($q.note) { $null = $sb.AppendLine(); $null = $sb.AppendLine("> $($q.note)") }
        $null = $sb.AppendLine()
    }
    $null = $sb.AppendLine("---")
    $null = $sb.AppendLine()
    $null = $sb.AppendLine("Generated by ``scripts/eval-rag.ps1``. Metric definitions and gate meanings: ``evals/README.md``.")

    Set-Content -Path $Path -Value $sb.ToString() -Encoding UTF8
}

function Get-Regression {
    <# Compares this run against a saved baseline result file. Missing -> no comparison. #>
    param($Current, [string] $BaselinePath, [double] $Tolerance)

    if (-not (Test-Path $BaselinePath)) {
        return [pscustomobject]@{ compared = $false; note = "no baseline at $BaselinePath"; regressions = @(); deltas = @() }
    }
    try { $base = Get-JsonFile $BaselinePath } catch { return [pscustomobject]@{ compared = $false; note = "baseline could not be read: $($_.Exception.Message)"; regressions = @(); deltas = @() } }

    $deltas = @()
    $regressions = @()
    foreach ($metric in @('precision', 'recall', 'f1', 'hit_rate', 'mrr')) {
        $was = ConvertTo-Number $base.aggregate.$metric
        $now = ConvertTo-Number $Current.$metric
        $delta = Round2 ($now - $was)
        $deltas += [pscustomobject]@{ metric = $metric; was = $was; now = $now; delta = $delta }
        if ($delta -lt (-1 * $Tolerance)) { $regressions += "$metric dropped $([math]::Abs($delta)) (was $was, now $now)" }
    }
    return [pscustomobject]@{
        compared    = $true
        note        = "baseline: $BaselinePath ($(Get-Prop $base 'started_at' 'unknown time'), k=$(Get-Prop $base 'k' '?'))"
        regressions = $regressions
        deltas      = $deltas
    }
}

# =============================================================================
# Main
# =============================================================================

try {
    Write-Host "== Book RAG: evaluation ==" -ForegroundColor Cyan

    $suitePath = $Suite
    if (-not (Test-Path $suitePath)) { Bad "suite not found: $suitePath"; exit 2 }

    try { $suiteData = Get-JsonFile $suitePath } catch { Bad "suite is not valid JSON: $($_.Exception.Message)"; exit 2 }

    $envMap = Get-EnvMap
    $db = Get-DbSettings $envMap

    # ---- catalogue: live if possible, sample CSV otherwise -------------------
    # Not in -ValidateOnly or -InputResults: those must never touch docker, so the
    # sample CSV stands in for the books table (the ids are the same).
    $catalogue = @()
    $catalogueSource = 'none'
    $dbUp = $false
    if (-not $ValidateOnly -and -not $InputResults) {
        $dbUp = Test-DbUp $db
        if ($dbUp) {
            $catalogue = Get-DbCatalogue $db
            $catalogueSource = "database ($($catalogue.Count) rows)"
        }
    }
    if ($catalogue.Count -eq 0) {
        $catalogue = Get-FileCatalogue 'samples\books.csv'
        if ($catalogue.Count -gt 0) { $catalogueSource = "samples\books.csv ($($catalogue.Count) rows)" }
    }
    Ok "catalogue: $catalogueSource"

    # ---- validate ------------------------------------------------------------
    # A per-question `k` in the suite always wins; -K overrides only the default.
    # -K is not available in -ValidateOnly (different parameter set), so guard on it.
    $kOverride = 0
    if ($PSBoundParameters.ContainsKey('K') -and $K -gt 0) { $kOverride = $K }
    $validation = Test-Suite $suiteData $catalogue $kOverride
    Show-Validation $validation $catalogue $validation.defaultK $suitePath

    if ($validation.errors.Count) {
        Write-Host "`nFAIL: the suite has $($validation.errors.Count) error(s); fix them before running it." -ForegroundColor Red
        exit 1
    }

    $apiKey = $envMap['GEMINI_API_KEY']
    if (-not $apiKey) { $apiKey = $env:GEMINI_API_KEY }
    $embedModel = if ($envMap['GEMINI_EMBEDDING_MODEL']) { $envMap['GEMINI_EMBEDDING_MODEL'] } else { 'models/gemini-embedding-001' }
    $embedDim = [int](ConvertTo-Number $envMap['EMBEDDING_DIM'] 3072)
    $baseUrl = 'http://localhost:5678'
    $headerName = $envMap['WEBHOOK_HEADER_NAME']
    $headerValue = $envMap['WEBHOOK_HEADER_VALUE']

    if ($ValidateOnly) {
        Section "What a real run would do"
        Info "-Mode retrieve : $($validation.questions.Count) embedding call(s) to $embedModel, then one SQL query per question"
        Info "-Mode agent    : about $($validation.questions.Count * 3) Gemini chat request(s) (roughly 3 per question), plus embedding calls"
        if (-not $apiKey) { Warn "GEMINI_API_KEY is not in .env - a real run would stop at the first question" }
        if (-not $dbUp)   { Info "docker container n8n-book-rag-db is not reachable from here - expected in a sandboxed shell" }
        if (-not $headerName -or -not $headerValue) { Warn "WEBHOOK_HEADER_NAME / WEBHOOK_HEADER_VALUE are not both set in .env - agent mode would get 403" }
        Write-Host "`nPASS: suite is valid ($($validation.questions.Count) questions, 0 errors, $($validation.warnings.Count) warning(s))." -ForegroundColor Green
        exit 0
    }

    # ---- replay a saved run: no docker, no API, no n8n -----------------------
    if ($InputResults) {
        if (-not (Test-Path $InputResults)) { Bad "results file not found: $InputResults"; exit 2 }
        try { $saved = Get-JsonFile $InputResults } catch { Bad "results file is not valid JSON: $($_.Exception.Message)"; exit 2 }

        Section "Replaying $InputResults"
        $questionResults = @()
        $errors = 0
        $drift = @()
        foreach ($q in @($saved.questions)) {
            $expectedIds = @($q.expected_ids | ForEach-Object { [string]$_ })
            $expectedTitles = @($q.expected_titles | ForEach-Object { [string]$_ })
            $returnedIds = @($q.returned_ids | ForEach-Object { [string]$_ })
            $returnedTitles = @($q.returned_titles | ForEach-Object { [string]$_ })
            $errorText = [string](Get-Prop $q 'error' '')
            if ($returnedTitles.Count -lt $returnedIds.Count) {
                $returnedTitles = @($returnedIds | ForEach-Object {
                    $title = [string]$catalogueTitlesFromMap[$_]
                    if ($title) { Get-DisplayTitle $title } else { $_ }
                })
            }

            # Metrics are RECOMPUTED from the saved answers, not trusted from the
            # file, so the gate thresholds are applied with the current rules. The
            # stored values are kept alongside for comparison: a difference means
            # the scoring rules changed since that run was saved. Numbers are read
            # with the INVARIANT culture, because JSON writes 0.74 as "0.74" and
            # this machine's culture would read that as 74.
            $metrics = Get-MetricSet -RankedIds $returnedIds -Expected $expectedIds
            $stored = Get-Prop $q 'metrics' $null
            if ($null -ne $stored) {
                $storedPrecision = ConvertTo-Number (Get-Prop $stored 'precision' -1)
                $storedRecall = Get-Prop $stored 'recall' $null
                $recallMatches = ($null -eq $storedRecall -and $null -eq $metrics.recall) -or
                                 ($null -ne $storedRecall -and $null -ne $metrics.recall -and ([math]::Abs((ConvertTo-Number $storedRecall) - $metrics.recall) -lt 0.005))
                if ($storedPrecision -ne $metrics.precision -or -not $recallMatches) {
                    $drift += "$($q.id): saved $(Show-Num $storedPrecision)/$(Show-Num $storedRecall) vs recomputed $(Show-Num $metrics.precision)/$(Show-Num $metrics.recall)"
                }
            }

            $passed = [bool](Get-Prop $q 'passed' $false)
            if ($errorText) { $passed = $false; $errors++ }

            $questionResults += [pscustomobject]@{
                id              = [string](Get-Prop $q 'id' '')
                category        = [string](Get-Prop $q 'category' 'taste')
                question        = [string](Get-Prop $q 'question' '')
                note            = [string](Get-Prop $q 'note' '')
                k               = [int](ConvertTo-Number (Get-Prop $q 'k' $validation.defaultK) $validation.defaultK)
                mode            = [string](Get-Prop $q 'mode' 'retrieve')
                expected_ids    = $expectedIds
                expected_titles = $expectedTitles
                also_ids        = @($q.also_ids | ForEach-Object { [string]$_ })
                returned_ids    = $returnedIds
                returned_titles = $returnedTitles
                scores          = @(Get-Prop $q 'scores' @())
                missing_titles  = @($q.missing_titles | ForEach-Object { [string]$_ })
                answer_text     = [string](Get-Prop $q 'answer_text' '')
                metrics         = $metrics
                passed          = $passed
                reasons         = @($q.reasons | ForEach-Object { [string]$_ })
                error           = $errorText
            }
        }

        $run = [pscustomobject]@{
            suite            = [string](Get-Prop $saved 'suite' (Get-Prop $suiteData 'suite' 'unknown'))
            suite_version    = [int](ConvertTo-Number (Get-Prop $saved 'suite_version' 0))
            started_at       = [string](Get-Prop $saved 'started_at' 'unknown')
            replayed_at      = (Get-Date).ToString('yyyy-MM-dd HH:mm:ss')
            replayed_from    = $InputResults
            mode             = [string](Get-Prop $saved 'mode' 'replay')
            k                = [int](ConvertTo-Number (Get-Prop $saved 'k' $validation.defaultK) $validation.defaultK)
            embed_model      = [string](Get-Prop $saved 'embed_model' 'unknown')
            embed_dim        = [int](ConvertTo-Number (Get-Prop $saved 'embed_dim' 0))
            base_url         = [string](Get-Prop $saved 'base_url' '')
            catalogue_source = $catalogueSource
            catalogue_count  = $catalogue.Count
            questions        = $questionResults
            errors           = @($questionResults | Where-Object { $_.error }).Count
            stored_totals    = Get-Prop $saved 'totals' $null
            stored_aggregate = Get-Prop $saved 'aggregate' $null
            totals           = $null
            aggregate        = $null
            by_category      = $null
            gates            = @()
            verdict          = 'FAIL'
        }
        $reportMode = 'replay'
        Ok "replayed $($questionResults.Count) question(s); gates and report are recomputed over the saved answers"
    }
    else {

    # ---- preflight for a real run -------------------------------------------
    Section "Preflight"
    if (-not $apiKey) { Bad "GEMINI_API_KEY not found in .env (embeddings are required in every mode)"; exit 2 }
    $needDb = $Mode -in @('retrieve', 'both')
    if ($needDb -and -not $dbUp) {
        Bad "container n8n-book-rag-db is not running - start it with .\scripts\start-db.ps1 (or use -ValidateOnly)"
        exit 2
    }
    if ($needDb -and $catalogue.Count -eq 0) { Bad "the books table is empty - ingest the catalogue first (.\scripts\ingest-catalogue.ps1)"; exit 2 }
    if ($Mode -in @('agent', 'both')) {
        if (-not $headerName -or -not $headerValue) { Bad "WEBHOOK_HEADER_NAME / WEBHOOK_HEADER_VALUE missing from .env"; exit 2 }
        try { $null = Invoke-RestMethod -Uri "$baseUrl/healthz" -TimeoutSec 10 }
        catch { Bad "n8n is not answering on $baseUrl - start it, and make the workflow Active"; exit 2 }
    }
    Ok "embeddings: $embedModel ($embedDim dims)"
    if ($needDb) { Ok "database: n8n-book-rag-db, $($catalogue.Count) books" }
    if ($Mode -in @('agent', 'both')) { Ok "agent endpoint: POST $baseUrl/webhook/book-rag/recommend" }

    # Title map for agent-mode matching; ambiguous normalised titles are called out.
    $titleMap = Get-TitleMapFromCatalogue $catalogue
    $ambiguous = @($titleMap.Keys | Where-Object { @($titleMap[$_]).Count -gt 1 })
    foreach ($a in $ambiguous) { Warn "titles '$a' are indistinguishable after normalisation ($(Join-Ids $titleMap[$a])); agent-mode scoring treats them as one" }

    $startedAt = (Get-Date).ToString('yyyy-MM-dd HH:mm:ss')
    $sessionStamp = (Get-Date).ToString('yyyyMMdd-HHmmss')
    $modes = if ($Mode -eq 'both') { @('retrieve', 'agent') } else { @($Mode) }

    # book_id -> title, for readable reports.
    $catalogueTitlesFromMap = @{}
    foreach ($b in $catalogue) { $catalogueTitlesFromMap[$b.book_id] = $b.title }

    $questionResults = @()
    $errors = 0

    foreach ($modeName in $modes) {
        Section "Running $($validation.questions.Count) question(s) in '$modeName' mode"
        $i = 0
        foreach ($q in $validation.questions) {
            $i++
            $label = "[{0,2}/{1}] {2,-24}" -f $i, $validation.questions.Count, $q.id
            $expectedTitles = @($q.expected_ids | ForEach-Object {
                $title = [string]$catalogueTitlesFromMap[$_]
                if ($title) { Get-DisplayTitle $title } else { $_ }
            })

            $result = [pscustomobject]@{
                id              = $q.id
                category        = $q.category
                question        = $q.question
                note            = $q.note
                k               = $q.k
                mode            = $modeName
                expected_ids    = $q.expected_ids
                expected_titles = $expectedTitles
                also_ids        = $q.also_ids
                returned_ids    = @()
                returned_titles = @()
                scores          = @()
                missing_titles  = @()
                answer_text     = ''
                metrics         = $null
                passed          = $false
                reasons         = @()
                error           = ''
            }

            try {
                if ($modeName -eq 'retrieve') {
                    $embedding = Invoke-GeminiEmbedding -Text $q.question -ApiKey $apiKey -Model $embedModel -TimeoutSec $TimeoutSec
                    if (@($embedding).Count -ne $embedDim) {
                        throw "embedding has $(@($embedding).Count) dimensions but EMBEDDING_DIM=$embedDim"
                    }
                    $ranked = @(Get-RetrievedBooks -Embedding $embedding -K $q.k -Db $db)
                    $result.returned_ids    = @($ranked | ForEach-Object { $_.id })
                    $result.returned_titles = @($ranked | ForEach-Object { $_.title })
                    $result.scores          = @($ranked | ForEach-Object { $_.similarity })
                }
                else {
                    $sessionId = "eval-$($q.id)-$sessionStamp"
                    $answer = Invoke-AgentRecommend -Question $q.question -SessionId $sessionId -BaseUrl $baseUrl -Headers @{ $headerName = $headerValue } -TimeoutSec $TimeoutSec
                    $result.answer_text = $answer

                    # Which catalogue books did the agent actually name? Titles the
                    # question itself contains are excluded: echoing "Dune" back is
                    # not a recommendation, and the suite declares those ids in
                    # 'also_mentions' for exactly this reason.
                    $mentioned = @(Get-BookIdsMentioned -Text $answer -TitleMap $titleMap -Catalogue $catalogue | Where-Object { $q.also_ids -notcontains $_ })
                    $result.returned_ids = @($mentioned | Select-Object -First $q.k)
                    $result.returned_titles = @($result.returned_ids | ForEach-Object {
                        $title = [string]$catalogueTitlesFromMap[$_]
                        if ($title) { Get-DisplayTitle $title } else { $_ }
                    })
                }
            }
            catch {
                $result.error = $_.Exception.Message
                $errors++
            }

            if (-not $result.error) {
                $result.metrics = Get-MetricSet -RankedIds $result.returned_ids -Expected $q.expected_ids
                $result.missing_titles = @($result.metrics.missing | ForEach-Object {
                    $title = [string]$catalogueTitlesFromMap[$_]
                    if ($title) { Get-DisplayTitle $title } else { $_ }
                })

                # Gates. Recall is undefined for a refuse question, so precision
                # carries it; a self question additionally carries its own strict
                # recall, because missing your own book means retrieval is broken.
                $reasons = @()
                if ($q.category -eq 'refuse') {
                    if ($MinRefusePrecision -gt 0 -and $result.metrics.precision -lt $MinRefusePrecision) {
                        $reasons += "refuse precision $($result.metrics.precision) < $MinRefusePrecision (it named a book for an out-of-catalogue request)"
                    }
                }
                else {
                    if ($MinPrecision -gt 0 -and $result.metrics.precision -lt $MinPrecision) {
                        $reasons += "precision@$($q.k) $($result.metrics.precision) < $MinPrecision"
                    }
                    if ($MinRecall -gt 0 -and $result.metrics.recall -lt $MinRecall) {
                        $reasons += "recall@$($q.k) $($result.metrics.recall) < $MinRecall"
                    }
                    if ($MinHitRate -gt 0 -and $null -ne $result.metrics.hit -and $result.metrics.hit -lt 1) {
                        $reasons += "no expected book in the top $($q.k)"
                    }
                }
                if ($q.category -eq 'self' -and $MinSelfRecall -gt 0 -and $result.metrics.recall -lt $MinSelfRecall) {
                    $reasons += "self-recall $($result.metrics.recall) < $MinSelfRecall"
                }
                $result.reasons = $reasons
                $result.passed = ($reasons.Count -eq 0)
            }

            $questionResults += $result

            if ($result.error) {
                Bad "$label ERROR $($result.error)"
            }
            elseif ($result.passed) {
                Ok "$label prec $($result.metrics.precision)  rec $($result.metrics.recall)  hit $($result.metrics.hit)  -> $(Join-Ids $result.returned_titles)"
            }
            else {
                Warn "$label prec $($result.metrics.precision)  rec $($result.metrics.recall)  hit $($result.metrics.hit)  -> $(Join-Ids $result.returned_titles)"
                foreach ($reason in $result.reasons) { Info "        $reason" }
                if ($result.missing_titles.Count) { Info "        missed: $(Join-Ids $result.missing_titles)" }
            }
        }
    }

    } # end of the live-run branch (the replay branch above already built $run)

    # ---- aggregate -----------------------------------------------------------
    if (-not $InputResults) {
    $run = [pscustomobject]@{
        suite            = [string](Get-Prop $suiteData 'suite' 'unknown')
        suite_version    = [int](ConvertTo-Number (Get-Prop $suiteData 'version' 0))
        started_at       = $startedAt
        mode             = $Mode
        k                = $validation.defaultK
        embed_model      = $embedModel
        embed_dim        = $embedDim
        base_url         = $baseUrl
        catalogue_source = $catalogueSource
        catalogue_count  = $catalogue.Count
        questions        = $questionResults
        errors           = $errors
        totals           = $null
        aggregate        = $null
        by_category      = $null
        gates            = @()
        verdict          = 'FAIL'
    }
    $reportMode = $Mode
    }

    $scored = @($questionResults | Where-Object { -not $_.error })
    $run.totals = [pscustomobject]@{
        questions  = $questionResults.Count
        scored     = $scored.Count
        errors     = $errors
        passed     = @($scored | Where-Object { $_.passed }).Count
        failed     = @($scored | Where-Object { -not $_.passed }).Count
    }
    $run.aggregate = Get-Aggregate $scored

    $byCategory = [ordered]@{}
    foreach ($c in $script:ValidCategories) {
        $subset = @($scored | Where-Object { $_.category -eq $c })
        if ($subset.Count) { $byCategory[$c] = Get-Aggregate $subset }
    }
    $run.by_category = [pscustomobject]$byCategory

    # ---- gates ---------------------------------------------------------------
    $selfQuestions = @($scored | Where-Object { $_.category -eq 'self' })
    $selfRecall = if ($selfQuestions.Count) { Get-Aggregate $selfQuestions } else { $null }
    $refuseQuestions = @($scored | Where-Object { $_.category -eq 'refuse' })
    $refuseAgg = if ($refuseQuestions.Count) { Get-Aggregate $refuseQuestions } else { $null }

    $gates = @()
    foreach ($g in @(
        @{ name = "questions passed";   threshold = "all";                          actual = "$($run.totals.passed)/$($scored.Count)"; ok = ($run.totals.failed -eq 0) },
        @{ name = "precision@k (mean)"; threshold = (Show-Num $MinPrecision);       actual = (Show-Num $run.aggregate.precision); ok = ($MinPrecision -le 0 -or $run.aggregate.precision -ge $MinPrecision) },
        @{ name = "recall@k (mean)";    threshold = (Show-Num $MinRecall);          actual = (Show-Num $run.aggregate.recall);    ok = ($MinRecall -le 0 -or $run.aggregate.recall -ge $MinRecall) },
        @{ name = "hit rate";           threshold = (Show-Num $MinHitRate);         actual = (Show-Num $run.aggregate.hit_rate);  ok = ($MinHitRate -le 0 -or $run.aggregate.hit_rate -ge $MinHitRate) },
        @{ name = "self recall (exact titles)"; threshold = (Show-Num $MinSelfRecall); actual = $(if ($selfRecall) { Show-Num $selfRecall.recall } else { 'n/a' }); ok = (-not $selfRecall -or $MinSelfRecall -le 0 -or $selfRecall.recall -ge $MinSelfRecall) },
        @{ name = "refuse precision";   threshold = (Show-Num $MinRefusePrecision); actual = $(if ($refuseAgg) { Show-Num $refuseAgg.precision } else { 'n/a' }); ok = (-not $refuseAgg -or $MinRefusePrecision -le 0 -or $refuseAgg.precision -ge $MinRefusePrecision) },
        @{ name = "errors";             threshold = "<= $MaxErrors";                actual = "$errors";                     ok = ($errors -le $MaxErrors) }
    )) {
        $gates += [pscustomobject]@{ name = $g.name; threshold = $g.threshold; actual = $g.actual; ok = [bool]$g.ok }
    }
    $run.gates = $gates

    # ---- comparison with a saved baseline ------------------------------------
    $regression = Get-Regression -Current $run.aggregate -BaselinePath $Baseline -Tolerance $MaxRegression

    # ---- console summary -----------------------------------------------------
    Section "Results ($reportMode mode, k=$($validation.defaultK))"
    Write-Host ""
    Write-Host ("  {0,-12} {1,-10} {2,-9} {3,-9} {4,-8} {5}" -f 'category', 'questions', 'prec@k', 'recall@k', 'hit', 'MRR') -ForegroundColor White
    # Taken from the per-question metrics via Get-Aggregate, not from the stored
    # aggregate: that is the single place where nulls are excluded, so a 'refuse'
    # question shows '-' instead of a misleading 0.
    $tableRows = @([pscustomobject]@{ name = 'ALL'; agg = (Get-Aggregate $scored) })
    foreach ($c in $script:ValidCategories) {
        $subset = @($scored | Where-Object { $_.category -eq $c })
        if ($subset.Count) { $tableRows += [pscustomobject]@{ name = $c; agg = (Get-Aggregate $subset) } }
    }
    foreach ($row in $tableRows) {
        Write-Host ("  {0,-12} {1,-10} {2,-9} {3,-9} {4,-8} {5}" -f $row.name, $row.agg.questions, (Show-Num $row.agg.precision), (Show-Num $row.agg.recall), (Show-Num $row.agg.hit_rate), (Show-Num $row.agg.mrr))
    }
    Write-Host ""
    foreach ($g in $gates) {
        $line = "  {0,-30} threshold {1,-8} actual {2}" -f $g.name, $g.threshold, $g.actual
        if ($g.ok) { Ok $line } else { Bad $line }
    }

    $failedRows = @($scored | Where-Object { -not $_.passed })
    if ($failedRows.Count) {
        Section "Failing questions ($($failedRows.Count))"
        foreach ($f in $failedRows) {
            Warn "$($f.id) [$($f.category)] -> $(Join-Ids $f.returned_titles)"
            foreach ($r in $f.reasons) { Info "     $r" }
            if ($f.missing_titles.Count) { Info "     missed: $(Join-Ids $f.missing_titles)" }
        }
    }

    if ($regression.compared) {
        Section "Baseline comparison"
        Info $regression.note
        foreach ($d in $regression.deltas) {
            $colour = if ($d.delta -lt (-1 * $MaxRegression)) { 'Red' } elseif ($d.delta -gt 0) { 'Green' } else { 'Gray' }
            Write-Host ("  {0,-10} {1,6} -> {2,6}  ({3})" -f $d.metric, (Show-Num $d.was), (Show-Num $d.now), (Show-Num $d.delta)) -ForegroundColor $colour
        }
        foreach ($r in $regression.regressions) { Bad "regression: $r" }
    }
    elseif ($Baseline) {
        Info "baseline: $Baseline does not exist yet - nothing to compare against (see docs/USAGE.md on promoting a run)"
    }

    # ---- report --------------------------------------------------------------
    # The verdict is decided BEFORE the report is written, so the saved JSON and
    # Markdown carry the real result rather than a placeholder.
    $gateFailure = @($gates | Where-Object { -not $_.ok })
    $ok = ($gateFailure.Count -eq 0) -and ($regression.regressions.Count -eq 0) -and ($errors -le $MaxErrors)
    $run.verdict = if ($ok) { 'PASS' } else { 'FAIL' }

    $reportPaths = @()
    if ($OutDir -and $OutDir -ne '0') {
        if (-not (Test-Path $OutDir)) { $null = New-Item -ItemType Directory -Path $OutDir -Force }
        $stamp = (Get-Date).ToString('yyyyMMdd-HHmmss')
        $jsonPath = Join-Path $OutDir "rag-eval-$stamp-$reportMode.json"
        $mdPath   = Join-Path $OutDir "rag-eval-$stamp-$reportMode.md"
        $run | ConvertTo-Json -Depth 12 | Set-Content -Path $jsonPath -Encoding UTF8
        New-MarkdownReport -Run $run -Path $mdPath
        $reportPaths = @($jsonPath, $mdPath)
    }

    # ---- verdict -------------------------------------------------------------
    if ($InputResults) {
        Section "Replay comparison"
        $storedPrecision = ConvertTo-Number (Get-Prop $run.stored_aggregate 'precision' $null)
        $storedRecall = ConvertTo-Number (Get-Prop $run.stored_aggregate 'recall' $null)
        Info "saved aggregate  : precision $(Show-Num $storedPrecision), recall $(Show-Num $storedRecall)"
        Info "recomputed       : precision $(Show-Num $run.aggregate.precision), recall $(Show-Num $run.aggregate.recall)"
        if ($storedPrecision -ne $run.aggregate.precision -or $storedRecall -ne $run.aggregate.recall) {
            Warn "the saved aggregate differs from the recomputed one - the scoring code changed since that run was saved"
        }
        foreach ($d in $drift) { Warn "metric drift - $d" }
        if ($drift.Count -eq 0) { Ok "every saved per-question metric matches a fresh computation" }
    }

    # Baseline promotion: only a PASSING live run may become the reference, so a bad
    # day can never be baked in as the new normal.
    if ($PromoteBaseline) {
        Section "Baseline promotion"
        if ($InputResults) {
            Warn "-PromoteBaseline is ignored for -InputResults: promote the live run, not a replay"
        }
        elseif (-not $ok) {
            Warn "not promoting: the run FAILED, and a failing run must never become the baseline"
        }
        else {
            $baselineDir = Split-Path -Parent $Baseline
            if ($baselineDir -and -not (Test-Path $baselineDir)) { $null = New-Item -ItemType Directory -Path $baselineDir -Force }
            $run | ConvertTo-Json -Depth 12 | Set-Content -Path $Baseline -Encoding UTF8
            Ok "wrote baseline: $Baseline"
        }
    }

    Section "Verdict"
    if ($ok) { Write-Host "  PASS: every gate held ($($run.totals.passed)/$($scored.Count) questions, $errors error(s))." -ForegroundColor Green }
    else {
        Write-Host "  FAIL: $($run.totals.failed) question(s) below threshold, $($gateFailure.Count) gate(s) missed, $($regression.regressions.Count) regression(s), $errors error(s)." -ForegroundColor Red
    }
    foreach ($p in $reportPaths) { Info "report: $p" }
    Info "metrics are defined in evals/README.md; re-score a saved report with scripts/eval_metrics.py"

    if ($ok) { exit 0 } else { exit 1 }
}
finally {
    Pop-Location
}
