[CmdletBinding()]
param(
    [string]$BaseUrl = "http://127.0.0.1:5001",
    [string]$ApiKey = $env:DEEPSEEK_WEB_TO_API_API_KEY,
    [string[]]$Models = @("deepseek-v4-pro", "deepseek-v4-pro-nothinking", "deepseek-v4-flash", "deepseek-v4-flash-nothinking"),
    [int[]]$ProbeSizes = @(150000, 160000, 163839, 163840, 163841, 170000),
    [int]$MaxTokens = 4,
    [int]$TimeoutSeconds = 180,
    [bool]$StopOnAccountBan = $true,
    [bool]$StopOnAuthFailure = $true,
    [bool]$StopOnRateLimit = $true,
    [string]$OutputJson = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Net.Http

function New-NoProxyHttpClient {
    $handler = [System.Net.Http.HttpClientHandler]::new()
    $handler.UseProxy = $false
    $handler.AllowAutoRedirect = $true
    $client = [System.Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromSeconds($TimeoutSeconds)
    return $client
}

function Get-AccountState([string]$Body) {
    if ([string]::IsNullOrWhiteSpace($Body)) {
        return ""
    }
    if ($Body -match '40012|"code"\s*:\s*10|permanent(?:ly)?[_ -]?ban|account.*(?:ban|suspend|disable)|\u5C01\u53F7|\u5C01\u7981') {
        return "permanently_banned"
    }
    if ($Body -match '50006|mute_until|temporary(?:ily)?[_ -]?(?:mute|ban)|muted|silenced|\u7981\u8A00') {
        return "temporarily_muted"
    }
    return ""
}

function Classify-Result([int]$Status, [string]$Body) {
    $accountState = Get-AccountState $Body
    if ($accountState) {
        return "account_$accountState"
    }
    if ($Status -eq 413) {
        return "local_prompt_limit"
    }
    if ($Status -eq 401) {
        return "token_invalid_or_relogin"
    }
    if ($Status -eq 403) {
        return "permission_or_local_policy_rejected"
    }
    if ($Status -eq 429) {
        if ($Body -match "upstream_empty_output|rate_limit_exceeded|rate limit|rate_limit_error|retry-after") {
            return "temporary_rate_limit"
        }
        return "upstream_rate_limited_or_rejected"
    }
    if ($Status -ge 200 -and $Status -lt 300) {
        return "success"
    }
    return "other_error"
}

function Get-ObjectProperty($Object, [string]$Name) {
    if ($null -eq $Object) {
        return $null
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return $null
    }
    return $property.Value
}

if ([string]::IsNullOrWhiteSpace($ApiKey)) {
    throw "ApiKey is required; pass -ApiKey or set DEEPSEEK_WEB_TO_API_API_KEY"
}

$uri = $BaseUrl.TrimEnd("/") + "/v1/chat/completions"
$client = New-NoProxyHttpClient
$results = [System.Collections.Generic.List[object]]::new()
$abortAll = $false
try {
    foreach ($model in $Models) {
        if ($abortAll) {
            break
        }
        foreach ($size in $ProbeSizes) {
            $suffix = "`nReturn exactly OK."
            if ($size -lt $suffix.Length) {
                throw "Probe size $size is smaller than the fixed suffix length $($suffix.Length)"
            }
            $prompt = [String]::new([char]"x", $size - $suffix.Length) + $suffix
            $request = @{
                model = $model
                messages = @(@{ role = "user"; content = $prompt })
                stream = $false
                max_tokens = $MaxTokens
            } | ConvertTo-Json -Depth 20 -Compress
            $content = [System.Net.Http.StringContent]::new($request, [Text.Encoding]::UTF8, "application/json")
            $httpRequest = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Post, $uri)
            $httpRequest.Content = $content
            $httpRequest.Headers.TryAddWithoutValidation("Authorization", "Bearer $ApiKey") | Out-Null
            $httpRequest.Headers.TryAddWithoutValidation("Cache-Control", "no-cache") | Out-Null
            $httpRequest.Headers.TryAddWithoutValidation("X-DeepSeek-Web-To-API-Cache-Control", "bypass") | Out-Null
            $started = [Diagnostics.Stopwatch]::StartNew()
            try {
                $response = $client.SendAsync($httpRequest).GetAwaiter().GetResult()
                $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
                $json = $null
                try { $json = $body | ConvertFrom-Json } catch {}
                $errorObject = Get-ObjectProperty $json "error"
                $usageObject = Get-ObjectProperty $json "usage"
                $classification = Classify-Result ([int]$response.StatusCode) $body
                $results.Add([pscustomobject]@{
                    model = $model
                    requested_chars = $size
                    requested_utf16_units = $prompt.Length
                    requested_code_points = [int]([Text.Encoding]::UTF32.GetByteCount($prompt) / 4)
                    request_prompt_bytes = [Text.Encoding]::UTF8.GetByteCount($prompt)
                    thinking = -not $model.ToLowerInvariant().Contains("nothinking")
                    status = [int]$response.StatusCode
                    classification = $classification
                    account_state = Get-AccountState $body
                    elapsed_ms = $started.ElapsedMilliseconds
                    error_code = [string](Get-ObjectProperty $errorObject "code")
                    error_type = [string](Get-ObjectProperty $errorObject "type")
                    retry_after = if ($response.Headers.RetryAfter) { [string]$response.Headers.RetryAfter } else { "" }
                    prompt_tokens = [int](Get-ObjectProperty $usageObject "prompt_tokens")
                    body_preview = if ($body.Length -gt 600) { $body.Substring(0, 600) } else { $body }
                })
            } catch {
                $results.Add([pscustomobject]@{
                    model = $model
                    requested_chars = $size
                    requested_utf16_units = $prompt.Length
                    requested_code_points = [int]([Text.Encoding]::UTF32.GetByteCount($prompt) / 4)
                    request_prompt_bytes = [Text.Encoding]::UTF8.GetByteCount($prompt)
                    thinking = -not $model.ToLowerInvariant().Contains("nothinking")
                    status = 0
                    classification = "transport_error"
                    account_state = ""
                    elapsed_ms = $started.ElapsedMilliseconds
                    error_code = ""
                    error_type = ""
                    retry_after = ""
                    prompt_tokens = 0
                    body_preview = $_.Exception.Message
                })
            } finally {
                $httpRequest.Dispose()
            }
            Write-Output ("{0} {1} chars -> {2} ({3})" -f $model, $size, $results[$results.Count - 1].status, $results[$results.Count - 1].classification)
            $lastClass = [string]$results[$results.Count - 1].classification
            if ($StopOnAccountBan -and ($lastClass -eq "account_permanently_banned" -or $lastClass -eq "account_temporarily_muted")) {
                $abortAll = $true
                break
            }
            if ($StopOnAuthFailure -and $lastClass -eq "token_invalid_or_relogin") {
                $abortAll = $true
                break
            }
            if ($StopOnRateLimit -and ($lastClass -eq "temporary_rate_limit" -or $lastClass -eq "upstream_rate_limited_or_rejected")) {
                break
            }
        }
    }
} finally {
    $client.Dispose()
}

$jsonOutput = $results | ConvertTo-Json -Depth 10
if ($OutputJson) {
    $encoding = [System.Text.UTF8Encoding]::new($false)
    $outputPath = [System.IO.Path]::GetFullPath($OutputJson)
    $outputDirectory = [System.IO.Path]::GetDirectoryName($outputPath)
    if ($outputDirectory) {
        New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null
    }
    [System.IO.File]::WriteAllText($outputPath, $jsonOutput, $encoding)
} else {
    Write-Output $jsonOutput
}
