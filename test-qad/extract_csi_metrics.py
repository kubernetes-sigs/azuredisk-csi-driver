#!/usr/bin/env python3

"""
CSI Metrics Extractor  
Extract CSI request latencies from logs and create a separate report
"""

import json
import re
from pathlib import Path
from datetime import datetime

def main():
    log_dir = Path('/home/laandrea/go/src/github.com/azuredisk-csi-driver')
    log_files = list(log_dir.glob('qad_my_log_*.log'))
    
    csi_stage_latencies = []
    csi_unstage_latencies = []
    
    # Read all log files
    for log_file in sorted(log_files):
        with open(log_file, 'r') as f:
            for line in f:
                # Match stage_volume latency
                if 'Observed Request Latency' in line and 'node_stage_volume' in line:
                    match = re.search(r'latency_seconds=([\d.]+)', line)
                    if match:
                        latency = float(match.group(1))
                        csi_stage_latencies.append(latency)
                
                # Match unstage_volume latency
                if 'Observed Request Latency' in line and 'node_unstage_volume' in line:
                    match = re.search(r'latency_seconds=([\d.]+)', line)
                    if match:
                        latency = float(match.group(1))
                        csi_unstage_latencies.append(latency)
    
    def calculate_stats(values):
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
            'p95': sorted_vals[int(n * 0.95)] if n > 20 else sorted_vals[-1],
            'p99': sorted_vals[int(n * 0.99)] if n > 100 else sorted_vals[-1],
        }
        return stats
    
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
    
    # Save metrics JSON
    metrics_file = log_dir / 'test-qad' / 'csi_metrics.json'
    with open(metrics_file, 'w') as f:
        json.dump(metrics, f, indent=2)
    
    print(f"CSI Stage Volume (NodeStageVolume):")
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
    
    print(f"\nMetrics saved to: {metrics_file}")

if __name__ == '__main__':
    main()
