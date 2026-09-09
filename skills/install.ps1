# Install every skill in .\skills into the user-level skill directories that
# Claude Code and Codex read on Windows. Copies, never symlinks.
#
# Usage: .\install.ps1 [-Targets claude,codex] [-DryRun]

param(
    [string[]]$Targets = @('claude', 'codex'),
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path

function Get-Dest([string]$target) {
    switch ($target) {
        'claude' { Join-Path $HOME '.claude\skills' }
        'codex'  { Join-Path $HOME '.codex\skills' }
        default  { throw "unknown target: $target" }
    }
}

# Same checks as tests/lint.sh, kept minimal so Windows needs no bash.
Get-ChildItem (Join-Path $here 'skills') -Directory | ForEach-Object {
    $skill = Join-Path $_.FullName 'SKILL.md'
    if (-not (Test-Path $skill)) { throw "$($_.Name): missing SKILL.md" }
    $lines = Get-Content $skill
    if ($lines[0] -ne '---') { throw "$($_.Name): SKILL.md must start with frontmatter" }
    if (-not ($lines -match "^name:\s*$($_.Name)\s*$")) { throw "$($_.Name): frontmatter name must equal directory name" }
    if (-not ($lines -match '^description:')) { throw "$($_.Name): frontmatter needs a description" }
    if (-not ($lines -match '^user-invocable:\s*(true|false)\s*$')) { throw "$($_.Name): frontmatter needs user-invocable true or false" }
}

foreach ($target in $Targets) {
    $dest = Get-Dest $target
    Get-ChildItem (Join-Path $here 'skills') -Directory | ForEach-Object {
        $name = $_.Name
        $to = Join-Path $dest $name
        if ($DryRun) { Write-Output "would install $name -> $to"; return }
        New-Item -ItemType Directory -Force -Path $dest | Out-Null
        if (Test-Path $to) { Remove-Item -Recurse -Force $to }
        Copy-Item -Recurse $_.FullName $to
        Write-Output "installed $name -> $to"
    }
}
