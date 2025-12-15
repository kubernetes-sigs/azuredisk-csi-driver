#!/usr/bin/env python3

"""
CSI Metrics Extractor
Extracts CSI request latencies from logs and generates JSON metrics
"""

import json
import re
import sys
from pathlib import Path
from datetime import datetime
from collections import defaultdict

def extract_csi_metrics(log_files):
    """Extract CSI metrics from log files"""
    
    csi_stage_latencies = []
    csi_unstage_latencies = []
    
    # Pattern for CSI metrics from azure_metrics.go
    # "Observed Request Latency" latency_seconds=0.712751919 request="azuredisk_csi_driver_node_unstage_volume"
    csi_pattern = r'Observed Request Latency.*latency_seconds=([\d.]+).*request="(azuredisk_csi_driver_node_stage_volume|azuredisk_csi_driver_node_unstage_volume)"'
    
    for log_file in log_files:
        try:
            with open(log_file, 'r') as f:
                for line in f:
                    match = re.search(csi_pattern, line)
                    if match:
                        latency_seconds = float(match.group(1))
                        request_type = match.group(2)
                        
                        if 'stage_volume' in request_type:
                            csi_stage_latencies.append(latency_seconds)
                        elif 'unstage_volume' in request_type:
                            csi_unstage_latencies.append(latency_seconds)
        except Exception as e:
            print(f"Error reading {log_file}: {e}", file=sys.stderr)
    
    return csi_stage_latencies, csi_unstage_latencies

def calculate_stats(values):
    """Calculate statistics from a list of values"""
    if not values:
        return {}
    
    sorted_vals = sorted(values)
    n = len(sorted_vals)
    
    stats = {
        'count': n,
        'min': sorted_vals[0],
        'max': sorted_vals[-1],
        'avg': sum(sorted_vals) / n,
        'p50': sorted_vals[n // 2],
        'p95': sorted_vals[int(n * 0.95)],
        'p99': sorted_vals[int(n * 0.99)],
    }
    
    return stats

def main():
    log_dir = Path('/home/laandrea/go/src/github.com/azuredisk-csi-driver')
    log_files = list(log_dir.glob('qad_my_log_*.log'))
    
    if not log_files:
        print("No log files found", file=sys.stderr)
        sys.exit(1)
    
    print(f"Extracting CSI metrics from {len(log_files)} log files...")
    
    csi_stage_latencies, csi_unstage_latencies = extract_csi_metrics(log_files)
    
    print(f"Found {len(csi_stage_latencies)} stage operations")
    print(f"Found {len(csi_unstage_latencies)} unstage operations")
    
    stage_stats = calculate_stats(csi_stage_latencies)
    unstage_stats = calculate_stats(csi_unstage_latencies)
    
    metrics = {
        'timestamp': datetime.now().isoformat(),
        'csi_stage_volume': {
            'raw_values': csi_stage_latencies,
            'statistics': stage_stats
        },
        'csi_unstage_volume': {
            'raw_values': csi_unstage_latencies,
            'statistics': unstage_stats
        }
    }
    
    output_file = log_dir / 'test-qad' / 'csi_metrics.json'
    with open(output_file, 'w') as f:
        json.dump(metrics, f, indent=2)
    
    print(f"\nCSI Metrics Summary:")
    print(f"\nCSI Stage Volume (NodeStageVolume):")
    print(f"  Operations: {stage_stats.get('count', 0)}")
    if stage_stats:
        print(f"  Min: {stage_stats['min']:.3f}s")
        print(f"  P50: {stage_stats['p50']:.3f}s")
        print(f"  Avg: {stage_stats['avg']:.3f}s")
        print(f"  P95: {stage_stats['p95']:.3f}s")
        print(f"  P99: {stage_stats['p99']:.3f}s")
        print(f"  Max: {stage_stats['max']:.3f}s")
    
    print(f"\nCSI Unstage Volume (NodeUnstageVolume):")
    print(f"  Operations: {unstage_stats.get('count', 0)}")
    if unstage_stats:
        print(f"  Min: {unstage_stats['min']:.3f}s")
        print(f"  P50: {unstage_stats['p50']:.3f}s")
        print(f"  Avg: {unstage_stats['avg']:.3f}s")
        print(f"  P95: {unstage_stats['p95']:.3f}s")
        print(f"  P99: {unstage_stats['p99']:.3f}s")
        print(f"  Max: {unstage_stats['max']:.3f}s")
    
    print(f"\nMetrics saved to: {output_file}")

if __name__ == '__main__':
    main()
