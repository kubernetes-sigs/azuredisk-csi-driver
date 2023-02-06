<#

Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

#>

using namespace Microsoft.PowerShell.Commands

$KLogTraceRegEx = '(?:\[pod\/([^/]+)\/([^/]+)\]\s)?(\w)(\d{2})(\d{2})\s(\d{2}:\d{2}:\d{2}\.\d{6})\s+\d+\s([^:]+):(\d+)\]\s+(.*)'
$RawLogTraceRegEx  = '(\d{4}-\d{2}-\d{2}\s\d{2}:\d{2}:\d{2}\.\d+\s\+\d{4})\s+\w+\s+(\w+)\s+(.*)'

$CSIOperationRegEx = '"Observed Request Latency"\s(?:(\w+)="?([-\w\./]+)"?\s?)+'

$CSIErrorRegEx = 'GRPC failed: method: /csi\.v1\.Controller/((?:Controller)?(?:Create|Delete|Publish|Unpublish)Volume), request: (.*), error: rpc error: code = (\w+) desc = (.*)'
$AzureErrorRegEx = 'Retriable: (?:true|false), RetryAfter: (\d+)s, HTTPStatusCode: (\d+), RawError: (.*)'
$ClientThrottledRegEx = 'azure cloud provider throttled for operation (\w+) with reason "client throttled"'

class CSIMessage {
    [datetime]$Timestamp
    [string]$Pod
    [string]$Container
    [string]$Level
    [string]$File
    [int]$Line
    [string]$Message
}

class CSIOperation {
    [datetime]$StartTime
    [datetime]$EndTime
    [string]$Request
    [double]$Latency
    [string]$Result
    [hashtable]$Properties = @{}
}

class CSIError {
    [datetime]$Timestamp
    [string]$Request
    [object]$Parameters
    [string]$Message
    [string]$Reason
    [string]$ExtendedReason
    [int]$HttpStatusCode
    [int]$RetryAfter
    [object]$Detail
}

class CSIObjectMeasureInfo {
    [string]$Property
    [double]$Average
    [int]$Count
    [double]$Maximum
    [double]$Minimum
    [double]$StandardDeviation
    [double]$Sum
    [hashtable]$Percentiles = [ordered]@{}

    CSIObjectMeasureInfo([GenericMeasureInfo]$MeasureInfo) {
            $this.Average=$_.Average
            $this.Count=$_.Count
            $this.Maximum=$_.Maximum
            $this.Minimum=$_.Minimum
            $this.Property=$_.Property
            $this.StandardDeviation=$_.StandardDeviation
            $this.Sum=$_.Sum
    }
}

<#

.SYNOPSIS
    Converts a klog log trace to a CSIMessage.

.DESCRIPTION
    ConvertTo-CSIMessage converts log traces to CSIMessage objects. 

.PARAMETER InputObject
    The log traces to parse from the pipeline.

.PARAMETER Year
    The year the trace was written. Defaults to the current year.

.PARAMETER RawLog
    Specify to indicate a raw logging format is used instead of klog.

.INPUTS
    System.String. You can pipe strings containing log traces to this cmdlet..

.OUTPUTS
    CSIMessage.

.EXAMPLE

    PS> Get-Content /tmp/azuredisk.log | ConvertTo-CSIMessage

    This example converts a downloaded Azure Disk log to CSIMessage objects.

#>
function ConvertTo-CSIMessage {
    [OutputType("CSIMessage")]
    [CmdletBinding()]
    param (
        [Parameter(ValueFromPipeline)]
        [string]
        $InputObject,

        [Parameter()]
        [string]
        $Year = $([DateTime]::Now.ToString("yyyy")),

        [Parameter()]
        [switch]
        $RawLog
    )

    $input `
    | Select-String -Pattern $(if (-not $RawLog) { $KLogTraceRegEx } else { $RawLogTraceRegEx })
    | ForEach-Object {
        $MatchGroups = $_.Matches.Groups

        $CSIMessage = [CSIMessage]::new()

        if (-not $RawLog) {
            $CSIMessage.Pod = $MatchGroups[1].Value
            $CSIMessage.Container = $MatchGroups[2].Value
            $CSIMessage.Level = $MatchGroups[3].Value
            $CSIMessage.Timestamp = [datetime]::Parse($("{0}-{1}-{2}T{3}Z" -f $Year,$MatchGroups[4].Value,$MatchGroups[5].Value,$MatchGroups[6].Value))
            $CSIMessage.File = $MatchGroups[7].Value
            $CSIMessage.Line = [int]::Parse($MatchGroups[8].Value)
            $CSIMessage.Message = $MatchGroups[9].Value
        } else {
            $CSIMessage.Pod = ""
            $CSIMessage.Container = "azuredisk"
            $CSIMessage.Level = $MatchGroups[2].Value
            $CSIMessage.Timestamp = [datetime]::Parse($MatchGroups[1].Value)
            $CSIMessage.File = ""
            $CSIMessage.Line = 0
            $CSIMessage.Message = $MatchGroups[3].Value
        }

        $CSIMessage | Write-Output
    } `
    | Write-Output
}

<#

.SYNOPSIS
    Finds CSIMessage objects in the pipeline input.

.DESCRIPTION
    Select-CSIMessage find CSIMessage objects in the pipeline input.

.PARAMETER InputObject
    The CSIMessage objects to find from the pipeline.

.PARAMETER After
    The time after which to search for CSI operations. If not specified, the
    search starts from the earliest CSIMessage object.

.PARAMETER Before
    The time before which to search for CSI operations. If not specified, the
    search ends at the latest CSIMessage object.

.INPUTS
    None. This cmdlet does not accept input from the pipeline.

.OUTPUTS
    CSIMessage.

.EXAMPLE

    PS> Get-CSIMessage -Controller | Select-CSIMessage -After "2023-01-03" -Before "2023-01-04"

    This example gets the CSI controller messages on January 3rd, 2023.

#>
function Select-CSIMessage {
    [OutputType("CSIMessage")]
    [CmdletBinding()]
    param (
        [Parameter(ValueFromPipeline)]
        [CSIMessage]
        $InputObject,

        [Parameter()]
        [datetime]
        $After,

        [Parameter()]
        [datetime]
        $Before
    )

    if ($After -and $After.Kind -eq [System.DateTimeKind]::Unspecified) {
        $After = [datetime]::SpecifyKind($After, [System.DateTimeKind]::Local)
    }

    if ($Before -and $Before.Kind -eq [System.DateTimeKind]::Unspecified) {
        $Before = [datetime]::SpecifyKind($Before, [System.DateTimeKind]::Local)
    }

    $input `
    | Where-Object {
        ($null -eq $After -or $_.Timestamp -ge $After) `
            -and ($nul -eq $Before -or $_.Timestamp -le $Before)
    } `
    | Write-Output
}

<#

.SYNOPSIS
    Retrieves CSIMessage objects from the Azure Disk CSI Driver.

.DESCRIPTION
    Get-CSIMessage retrieves CSIMessage objects from log traces generated by
    the Azure Disk CSI Driver. 

.PARAMETER Controller
    If specified, Get-CSMessage retrieves log traces from the CSI controller
    pods.

.PARAMETER Node
    If specified, Get-CSMessage retrieves log traces from the CSI node pods.

.PARAMETER App
    If specified, Get-CSMessage uses the value as an app selector to find the
    pods from which to retrieve log traces.

.PARAMETER All
    If specified, Get-CSIMessage retrieves log traces from all containers in 
    the pod.

.PARAMETER Container
    The container in the pod from which to get log traces. Defaults to
    "azuredisk".

.PARAMETER Namespace
    The pod namespace. Defaults to "kube-system".

.PARAMETER Previous
    If specified, Get-CSIMessage returns log traces from the previous
    container instance.

.PARAMETER Year
    The year the trace was written. Defaults to the current year.

.PARAMETER After
    The time after which to search for CSI operations. If not specified, the
    search starts from the beginning of the specified traces.

.PARAMETER Before
    The time before which to search for CSI operations. If not specified, the
    search ends at the end of the specified traces.

.INPUTS
    None. This cmdlet does not accept input from the pipeline.

.OUTPUTS
    CSIMessage.

.EXAMPLE

    PS> Get-CSIMessage -Controller

    This command gets the CSI messages from the azuredisk container in the controller pod.

.EXAMPLE

    PS> Get-CSIMessage -Controller -Container csi-attacher

    This command gets the CSI messages from the csi-attacher container in the controller pod.

.EXAMPLE

    PS> Get-CSIMessage -Controller -All

    This command gets the CSI messages from the all containers in the controller pod.

.EXAMPLE

    PS> Get-CSIMessage -Node

    This command gets the current node pod CSI messages from the azuredisk container.

#>
function Get-CSIMessage {
    [OutputType("CSIMessage")]
    [CmdletBinding()]
    param (
        [Parameter(Mandatory=$true, ParameterSetName = "Controller")]
        [Parameter(Mandatory=$true, ParameterSetName = "ControllerAll")]
        [switch]
        $Controller,

        [Parameter(Mandatory=$true, ParameterSetName = "Node")]
        [Parameter(Mandatory=$true, ParameterSetName = "NodeAll")]
        [switch]
        $Node,

        [Parameter(Mandatory=$true, ParameterSetName = "App")]
        [Parameter(Mandatory=$true, ParameterSetName = "AppAll")]
        [string]
        $App,

        [Parameter(Mandatory=$true, ParameterSetName = "AppAll")]
        [Parameter(Mandatory=$true, ParameterSetName = "ControllerAll")]
        [Parameter(Mandatory=$true, ParameterSetName = "NodeAll")]
        [switch]
        $All,

        [Parameter(ParameterSetName = "App")]
        [Parameter(ParameterSetName = "Controller")]
        [Parameter(ParameterSetName = "Node")]
        [string]
        $Container = "azuredisk",

        [Parameter()]
        [string]
        $Namespace = "kube-system",

        [Parameter()]
        [switch]
        $Previous,

        [Parameter()]
        [string]
        $Year = $([DateTime]::Now.ToString("yyyy")),

        [Parameter()]
        [DateTime]
        $After,

        [Parameter()]
        [DateTime]
        $Before
    )

    $KubectlLogs = "kubectl logs --prefix --namespace=`"{0}`" --tail=-1" -f $Namespace

    if ($Controller) {
        $App = "csi-azuredisk-controller"
    } elseif ($Node) {
        $App = "csi-azuredisk-controller"
    }
    $KubectlLogs += " --selector=app={0}" -f $App

    if ($All) {
        $KubectlLogs += " --all-containers"
    } else {
        $KubectlLogs += " --container={0}" -f $Container
    }

    if ($Previous) {
        $KubectlLogs += " --previous"
    }

    $SelectParams = @{}

    if ($After) {
        if ($After.Kind -eq [System.DateTimeKind]::Unspecified) {
            $After = [datetime]::SpecifyKind($After, [System.DateTimeKind]::Local)
        }

        $KubectlLogs += " --since-time=`"{0}`"" -f $After.ToString("yyyy-MM-ddTHH:mm:ss.FFFK")
        $SelectParams.add("After", $After)
    }

    if ($Before) {
        if ($After.Kind -eq [System.DateTimeKind]::Unspecified) {
            $After = [datetime]::SpecifyKind($After, [System.DateTimeKind]::Local)
        }

        $SelectParams.add("Before", $Before)
    }

    Invoke-Expression $KubectlLogs `
    | ConvertTo-CSIMessage `
    | Select-CSIMessage @SelectParams
    | Write-Output
}

<#
.SYNOPSIS
    Finds CSI operation instances in CSI log traces.

.PARAMETER InputObject
    The log traces to parse from the pipeline.

.PARAMETER Path
    The path to a file containing log traces.

.PARAMETER RawLog
    Specify to indicate a raw logging format is used instead of klog.

.PARAMETER Year
    The year the trace was written. Defaults to the current year.

.PARAMETER After
    The time after which to search for CSI operations. If not specified, the
    search starts from the beginning of the specified traces.

.PARAMETER Before
    The time before which to search for CSI operations. If not specified, the
    search ends at the end of the specified traces.

.INPUTS
    CSIMessage. You can pipe CSIMessage objects to this cmdlet.

.OUTPUTS
    CSIOperation.

.EXAMPLE

    PS> Get-CSIMessage -Controller | Find-CSIOperation

    This command gets all CSI operations from the controller pod.

.EXAMPLE

    PS> Find-CSIOperation -Path /tmp/csi-azuredisk-controller.log

    This command gets all CSI operations from a file containing the CSI controller logs.

#>
function Find-CSIOperation {
    [OutputType("CSIOperation")]
    [CmdletBinding(DefaultParameterSetName="FromPipeline")]
    param (
        [Parameter(ValueFromPipeline, ParameterSetName="FromPipeline")]
        [CSIMessage]
        $InputObject,

        [Parameter(Mandatory=$true, Position=0, ParameterSetName="FromFile")]
        [string]
        $Path,

        [Parameter(ParameterSetName="FromFile")]
        [switch]
        $RawLog,

        [Parameter()]
        [string]
        $Filter,

        [Parameter()]
        [string]
        $Year = $([DateTime]::Now.ToString("yyyy")),

        [Parameter()]
        [DateTime]
        $After,

        [Parameter()]
        [DateTime]
        $Before
    )

    $SelectParams = @{}

    if ($After) {
        $KubectlLogs += " --since-time={0}" -f $After.ToString("O")
        $SelectParams.add("After", $After)
    }

    if ($Before) {
        $SelectParams.add("Before", $Before)
    }

    $OperationPattern = '"Observed Request Latency"'

    if ($null -ne $Filter) {
        $OperationPattern = "{0}.*{1}" -f $OperationPattern,$Filter
    }

    $(if (-not $Path) {
        $input | Where-Object { $_.Message -match $OperationPattern }
    } else {
        Get-Content -Path $Path `
        | Select-String -Pattern $OperationPattern -Raw `
        | ConvertTo-CSIMessage -RawLog:$RawLog
    }) `
    | ForEach-Object {
        $M = $_.Message | Select-String -Pattern $CSIOperationRegEx
        $Properties = @($M.Matches.Groups[1].Captures.Value)
        $Values = @($M.Matches.Groups[2].Captures.Value)

        $CSIOperation = [CSIOperation]::new()

        for ($i = 0; $i -lt $Properties.Length; ++$i) {
            $CSIOperation.Properties.add($Properties[$i], $Values[$i])
        }

        $CSIOperation.Latency = [double]::Parse($CSIOperation.Properties.latency_seconds)
        $CSIOperation.StartTime = $_.Timestamp - [timespan]::FromSeconds($CSIOperation.Latency)
        $CSIOperation.EndTime = $_.Timestamp
        $CSIOperation.Request = $CSIOperation.Properties.request
        $CSIOperation.Result = $(
            if ($CSIOperation.Properties.ContainsKey("result_code")) {
                $CSIOperation.Properties.result_code
            } else {
                "succeeded"
            }
        )

        $CSIOperation | Write-Output
    } `
    | Where-Object { ($null -eq $After -or $_.StartTime -ge $After) -and ($null -eq $Before -or $_.EndTime -le $Before) } `
    | Write-Output
}

<#
.SYNOPSIS
    Finds end-to-end CSI publish and unpublish operations in CSI log traces.

    The end-to-end operations measure the publish and unpublish latency from
    the perspective of Kubernetes. They are determined by sorting operations
    by volume and start time to form the sequence of attempted publish and
    unpublish calls to the CSI driver. The start of a sequence is defined as
    the first publish or unpublish call for a volume. If the starting
    operation succeeds, it constitutes the full sequence. Otherwise, the
    operations are scanned until the the first matching successful operation
    for the volume is found. If no matching successful operation is found
    before an opposite operation is encountered, the previous sequence ends
    with the last unsuccessful operation and a new start of sequence for the
    new operation is recorded.

.PARAMETER InputObject
    The log traces to parse from the pipeline.

.PARAMETER Path
    The path to a file containing log traces.

.PARAMETER RawLog
    Specify to indicate a raw logging format is used instead of klog.

.PARAMETER Year
    The year the trace was written. Defaults to the current year.

.PARAMETER After
    The time after which to search for CSI operations. If not specified, the
    search starts from the beginning of the specified traces.

.PARAMETER Before
    The time before which to search for CSI operations. If not specified, the
    search ends at the end of the specified traces.

.INPUTS
    CSIMessage. You can pipe CSIMessage objects to this cmdlet.

.OUTPUTS
    CSIOperation.

.EXAMPLE

    PS> Get-CSIMessage -Controller | Find-CSIE2EVolumeOperation

    This command gets all end-to-end CSI volume operations from the controller
    pod.

.EXAMPLE

    PS> Find-CSIE2EVolumeOperation -Path /tmp/csi-azuredisk-controller.log

    This command gets all end-to-end CSI volume operations from a file
    containing the CSI controller logs.

#>
function Find-CSIE2EVolumeOperation {
    [OutputType("CSIOperation")]
    [CmdletBinding(DefaultParameterSetName="FromPipeline")]
    param (
        [Parameter(ValueFromPipeline, ParameterSetName="FromPipeline")]
        [CSIMessage]
        $InputObject,

        [Parameter(Mandatory=$true, Position=0, ParameterSetName="FromFile")]
        [string]
        $Path,

        [Parameter(ParameterSetName="FromFile")]
        [switch]
        $RawLog,

        [Parameter()]
        [string]
        $Year = $([DateTime]::Now.ToString("yyyy")),

        [Parameter()]
        [DateTime]
        $After,

        [Parameter()]
        [DateTime]
        $Before
    )

    $(if ($PSCmdlet.ParameterSetName -eq "FromPipeline") {
        $input
    } else {
        Find-CSIOperation @PSBoundParameters -Filter 'request="azuredisk_csi_driver_controller_(?:un)?publish_volume"'
    })`
    | Group-Object {$_.Properties["volumeid"]} `
    | ForEach-Object {
        $Sequence = $null
        $_.Group `
        | Sort-Object StartTime `
        | ForEach-Object {
            if ($null -eq $Sequence) {
                $Sequence = $_.PSObject.Copy()
                $Sequence | Add-Member -MemberType NoteProperty -Name "VolumeId" -Value $($_.Properties["volumeid"])
                $Sequence | Add-Member -MemberType NoteProperty -Name "RetryCount" -Value 0
            } elseif ($_.Request -ne $Sequence.Request) {
                $Sequence | Write-Output

                $Sequence = $_.PSObject.Copy()
                $Sequence | Add-Member -MemberType NoteProperty -Name "VolumeId" -Value $($_.Properties["volumeid"])
                $Sequence | Add-Member -MemberType NoteProperty -Name "RetryCount" -Value 0
            } else {
                $Sequence.RetryCount += 1
                $Sequence.Result = $_.Result
                $Sequence.EndTime = $_.EndTime
                $Sequence.Latency = ($Sequence.EndTime - $Sequence.StartTime).TotalSeconds
            }

            if ($_.Result -eq "succeeded") {
                $Sequence | Write-Output

                $Sequence = $null
            }
        } `
        | Write-Output
    }
}
<#
.SYNOPSIS
    Finds CSI error instances in CSI log traces.

.PARAMETER InputObject
    The log traces to parse from the pipeline.

.PARAMETER Path
    The path to a file containing log traces.

.PARAMETER RawLog
    Specify to indicate a raw logging format is used instead of klog.

.PARAMETER Year
    The year the trace was written. Defaults to the current year.

.PARAMETER After
    The time after which to search for CSI operations. If not specified, the
    search starts from the beginning of the specified traces.

.PARAMETER Before
    The time before which to search for CSI operations. If not specified, the
    search ends at the end of the specified traces.

.INPUTS
    CSIMessage. You can pipe CSIMessage objects to this cmdlet.

.OUTPUTS
    CSIError.

.EXAMPLE

    PS> Get-CSIMessage -Controller | Find-CSIError

    This command gets all CSI errors from the controller pod.

.EXAMPLE

    PS> Find-CSIError -Path /tmp/csi-azuredisk-controller.log

    This command gets all CSI errors from a file containing the CSI controller logs.

#>
function Find-CSIError {
    [OutputType("CSIOperation")]
    [CmdletBinding(DefaultParameterSetName="FromPipeline")]
    param (
        [Parameter(ValueFromPipeline, ParameterSetName="FromPipeline")]
        [CSIMessage]
        $InputObject,

        [Parameter(Mandatory=$true, Position=0, ParameterSetName="FromFile")]
        [string]
        $Path,

        [Parameter(ParameterSetName="FromFile")]
        [switch]
        $RawLog,

        [Parameter()]
        [string]
        $Year = $([DateTime]::Now.ToString("yyyy")),

        [Parameter()]
        [DateTime]
        $After,

        [Parameter()]
        [DateTime]
        $Before
    )

    $SelectParams = @{}

    if ($After) {
        $KubectlLogs += " --since-time={0}" -f $After.ToString("O")
        $SelectParams.add("After", $After)
    }

    if ($Before) {
        $SelectParams.add("Before", $Before)
    }

    $(if (-not $Path) {
        $input | Where-Object { $_.Message -match "GRPC failed: method: /csi\.v1\.Controller" }
    } else {
        Get-Content -Path $Path `
        | Select-String -Pattern "GRPC failed: method: /csi\.v1\.Controller" -Raw `
        | ConvertTo-CSIMessage -RawLog:$RawLog
    }) `
    | ForEach-Object {
        $ErrorMatch = $_.Message | Select-String -Pattern $CSIErrorRegEx

        $CSIError = [CSIError]::new()

        $CSIError.Timestamp = $_.Timestamp
        $CSIError.Request = $ErrorMatch.Matches.Groups[1].Value
        $CSIError.Parameters = $ErrorMatch.Matches.Groups[2].Value | ConvertFrom-Json
        $CSIError.Message = $ErrorMatch.Matches.Groups[4].Value
        $CSIError.Reason = $ErrorMatch.Matches.Groups[3].Value

        $InnerErrorMatch = $CSIError.Message | Select-String -Pattern $AzureErrorRegEx

        if (-not $InnerErrorMatch.Matches.Success) {
            $CSIError.HttpStatusCode = 0
            $CSIError.RetryAfter = 0
        } else {
            $CSIError.HttpStatusCode = [int]::Parse($InnerErrorMatch.Matches.Groups[2].Value)
            $CSIError.RetryAfter = [int]::Parse($InnerErrorMatch.Matches.Groups[1].Value)

            $InnerError = $InnerErrorMatch.Matches.Groups[3].Value
 
            if (-not $InnerError.StartsWith("{")) {
                $ClientThrottleMatch = $InnerError | Select-String -Pattern $ClientThrottledRegEx

                if ($ClientThrottleMatch.Matches.Success) {
                    $CSIError.Reason = "ClientThrottled"
                    $CSIError.ExtendedReason = $ClientThrottleMatch.Matches.Groups[1].Value
                }
            } else {
                $Detail = $InnerError | ConvertFrom-Json

                $CSIError.Detail = $Detail.error
                $CSIError.Reason = $Detail.error.code

                if ($null -ne $Detail.error.details) {
                    $CSIError.ExtendedReason = "{0}/{1}" -f $Detail.error.details[0].code,$Detail.error.details[0].target
                }
            }
        }

        $CSIError | Write-Output
    } `
    | Where-Object { ($null -eq $After -or $_.StartTime -ge $After) -and ($null -eq $Before -or $_.EndTime -le $Before) } `
    | Write-Output
}


<#
.SYNOPSIS
    Returns the specified percentile value for a given set of numbers.
    
.DESCRIPTION
    This function expects a set of numbers passed as an array to the 'Sequence' parameter.  For a given percentile, passed as the 'Percentile' argument,
    it returns the calculated percentile value for the set of numbers.
    
.PARAMETER Sequence
    A array of integer and/or decimal values the function uses as the data set.

.PARAMETER Percentile
    The target percentile to be used by the function's algorithm. 
    
.EXAMPLE
    $values = 98.2,96.5,92.0,97.8,100,95.6,93.3
    Measure-Percentile -Sequence $values -Percentile 0.95
    
.NOTES
    Author: Jim Birley

    MIT License

    Copyright (c) 2022 Jim Birley

    Permission is hereby granted, free of charge, to any person obtaining a copy
    of this software and associated documentation files (the "Software"), to deal
    in the Software without restriction, including without limitation the rights
    to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
    copies of the Software, and to permit persons to whom the Software is
    furnished to do so, subject to the following conditions:

    The above copyright notice and this permission notice shall be included in all
    copies or substantial portions of the Software.

    THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
    IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
    FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
    AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
    LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
    OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
    SOFTWARE.

#>
function Measure-Percentile {
[CmdletBinding()]
    param (
        [Parameter(Mandatory)] 
        [Double[]]$Sequence,

        [Parameter(Mandatory)]
        [Double]$Percentile
    )

    $Sequence = $Sequence | Sort-Object
    [int]$N = $Sequence.Length
    Write-Verbose "N is $N"
    [Double]$Num = ($N - 1) * $Percentile + 1
    Write-Verbose "Num is $Num"
    if ($num -eq 1) {
        return $Sequence[0]
    } elseif ($num -eq $N) {
        return $Sequence[$N-1]
    } else {
        $k = [Math]::Floor($Num)
        Write-Verbose "k is $k"
        [Double]$d = $num - $k
        Write-Verbose "d is $d"
        return $Sequence[$k - 1] + $d * ($Sequence[$k] - $Sequence[$k - 1])
    }
}

<#

.SYNOPSIS
    Calculates a numeric property of a CSI object.

.PARAMETER InputObject
    The CSI objects to measure from the pipeline.

.PARAMETER Property
    The property to measure.

.PARAMETER Percentiles
    The quantile measures to calculate. If not specified, the p50, p90, p99 and p99.9
    quantiles will be measured.

.INPUTS
    PSCustomObject. The CSI objects to measure from the pipeline.

.OUTPUTS
    CSIObjectMeasureInfo.

#>
function Measure-CSIObject {
    [OutputType("CSIObjectMeasureInfo")]
    [CmdletBinding()]
    param (
        # The CSI objects to measure.
        [Parameter(Mandatory, ValueFromPipeline)]
        [PSCustomObject]
        $InputObject,

        # The property to measure.
        [Parameter(Mandatory, Position=1)]
        [string]
        $Property,

        # The quantile measures to calculate.
        [Parameter()]
        [double[]]
        $Percentiles = @(0.50, 0.90, 0.99, 0.999)
    )
    
    $Values = $input | ForEach-Object { $_.PSObject.Properties[$Property].Value }

    $Values `
    | Measure-Object -AllStats `
    | ForEach-Object {
        $MeasureInfo = [CSIObjectMeasureInfo]::new($_)

        $Percentiles `
        | ForEach-Object {
            $MeasureInfo.Percentiles.add($_*100.0, $(Measure-Percentile $Values $_))
        }

        $MeasureInfo | Write-Output
    } `
    | Write-Output
}

<#

.SYNOPSIS

    Calculates the latency statistics of CSI operations.

.PARAMETER InputObject
    The CSI operations to measure.

.INPUTS
    CSIOperation. You can pipe CSIOperations to this cmdlet.

.OUTPUTS
    CSIObjectMeasureInfo.

.EXAMPLE

    PS> Get-CSIMessage -Controller | Find-CSIOperation | Measure-CSIOperation

    This example measures the latency of all CSI operations from the controller pod.

#>
function Measure-CSIOperation {
    [OutputType("CSIObjectMeasureInfo")]
    [CmdletBinding()]
    param (
        [Parameter(ValueFromPipeline)]
        [CSIOperation]
        $InputObject
    )

    $input `
    | Group-Object Request,Result `
    | ForEach-Object {
        $_.Group `
        | Measure-CSIObject Latency `
        | Add-Member -MemberType NoteProperty -Name "Request" -Value $_.Values[0] -Passthru `
        | Add-Member -MemberType NoteProperty -Name "Result" -Value $_.Values[1] -Passthru `
        | ForEach-Object {
            $MeasureInfo = $_

            $MeasureInfo.Percentiles.GetEnumerator() | ForEach-Object {
                $MeasureInfo | Add-Member -MemberType NoteProperty -Name $("p{0}" -f $_.Key) -Value $_.Value
            }

            $MeasureInfo | Write-Output
        } `
        | Write-Output
    } `
    | Write-Output
}

<#

.SYNOPSIS

    Calculates the retry after time statistics by error reason of CSI errors.

.PARAMETER InputObject
    The CSI errors to measure.

.INPUTS
    CSIError. You can pipe CSIError to this cmdlet.

.OUTPUTS
    CSIObjectMeasureInfo.

.EXAMPLE

    PS> Get-CSIMessage -Controller | Find-CSIError | Measure-CSIError

    This example measures the retry after times of all CSI errors from the controller pod.

#>
function Measure-CSIError {
    [OutputType("CSIObjectMeasureInfo")]
    [CmdletBinding()]
    param (
        [Parameter(ValueFromPipeline)]
        [CSIError]
        $InputObject
    )

    $input `
    | Group-Object HttpStatusCode,Reason,ExtendedReason `
    | ForEach-Object {
        $_.Group `
        | Measure-CSIObject RetryAfter `
        | Add-Member -MemberType NoteProperty -Name "HttpStatusCode" -Value $_.Values[0] -Passthru `
        | Add-Member -MemberType NoteProperty -Name "Reason" -Value $_.Values[1] -Passthru `
        | Add-Member -MemberType NoteProperty -Name "ExtendedReason" -Value $_.Values[2] -Passthru `
        | ForEach-Object {
            $MeasureInfo = $_

            $MeasureInfo.Percentiles.GetEnumerator() | ForEach-Object {
                $MeasureInfo | Add-Member -MemberType NoteProperty -Name $("p{0}" -f $_.Key) -Value $_.Value
            }

            $MeasureInfo | Write-Output
        } `
        | Write-Output
    } `
    | Write-Output
}

Export-ModuleMember -Function ConvertTo-CSIMessage,Select-CSIMessage,Get-CSIMessage,Find-CSIOperation,Measure-CSIOperation,Find-CSIE2EVolumeOperation,Find-CSIError,Measure-CSIError
