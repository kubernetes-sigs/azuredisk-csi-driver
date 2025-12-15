#!/usr/bin/env python3

"""
QAD Metrics Extractor
Parses CSI node logs and extracts QAD attach/detach latency metrics
"""

import re
import json
import sys
import os
from pathlib import Path
from collections import defaultdict
from datetime import datetime
from typing import Dict, List, Tuple

class QADMetricsExtractor:
    """Extract QAD latency metrics from CSI logs"""
    
    # Regex pattern for the metrics line
    METRICS_PATTERN = r'Observed Request Latency.*?latency_seconds=([0-9.]+).*?request="([^"]+)".*?result_code="([^"]+)"'
    
    # Regex pattern for direct QAD latency observations from NodeStageVolume/NodeUnstageVolume
    # Pattern: "NodeStageVolume: Latency observed for attach operation of disk X is Y"
    # Pattern: "NodeUnStageVolume: Latency observed for detach operation of disk X is Y"
    QAD_STAGE_LATENCY_PATTERN = r'NodeStageVolume: Latency observed for attach operation of disk .* is ([0-9]+)'
    QAD_UNSTAGE_LATENCY_PATTERN = r'NodeUnStageVolume: Latency observed for detach operation of disk .* is ([0-9]+)'
    
    def __init__(self):
        self.metrics = defaultdict(list)
        self.stage_metrics = []
        self.unstage_metrics = []
        self.qad_attach_metrics = []  # Direct QAD attach latencies from NodeStageVolume
        self.qad_detach_metrics = []  # Direct QAD detach latencies from NodeUnstageVolume
    
    def parse_log_file(self, filepath: str) -> None:
        """Parse a single log file"""
        try:
            with open(filepath, 'r') as f:
                content = f.read()
                self.extract_metrics(content, filepath)
        except FileNotFoundError:
            print(f"Warning: File not found: {filepath}", file=sys.stderr)
        except Exception as e:
            print(f"Error reading {filepath}: {e}", file=sys.stderr)
    
    def extract_metrics(self, content: str, source: str) -> None:
        """Extract metrics from log content"""
        matches = re.findall(self.METRICS_PATTERN, content)
        
        for match in matches:
            latency_str, request_type, result_code = match
            
            try:
                latency = float(latency_str)
            except ValueError:
                continue
            
            metric_entry = {
                'source': source,
                'request': request_type,
                'latency_seconds': latency,
                'result_code': result_code,
                'timestamp': datetime.now().isoformat()
            }
            
            self.metrics[request_type].append(metric_entry)
            
            if 'stage' in request_type:
                self.stage_metrics.append(metric_entry)
            elif 'unstage' in request_type:
                self.unstage_metrics.append(metric_entry)
        
        # Extract direct QAD attach latencies from NodeStageVolume logs
        stage_matches = re.findall(self.QAD_STAGE_LATENCY_PATTERN, content)
        for latency_ms in stage_matches:
            try:
                latency_sec = float(latency_ms) / 1000.0
            except ValueError:
                continue
            
            qad_entry = {
                'source': source,
                'operation': 'attach',
                'latency_seconds': latency_sec,
                'latency_milliseconds': float(latency_ms),
                'timestamp': datetime.now().isoformat()
            }
            self.qad_attach_metrics.append(qad_entry)
        
        # Extract direct QAD detach latencies from NodeUnstageVolume logs
        unstage_matches = re.findall(self.QAD_UNSTAGE_LATENCY_PATTERN, content)
        for latency_ms in unstage_matches:
            try:
                latency_sec = float(latency_ms) / 1000.0
            except ValueError:
                continue
            
            qad_entry = {
                'source': source,
                'operation': 'detach',
                'latency_seconds': latency_sec,
                'latency_milliseconds': float(latency_ms),
                'timestamp': datetime.now().isoformat()
            }
            self.qad_detach_metrics.append(qad_entry)
    
    def get_statistics(self, metrics_list: List[Dict]) -> Dict:
        """Calculate statistics for a list of metrics"""
        if not metrics_list:
            return {}
        
        latencies = [m['latency_seconds'] for m in metrics_list]
        latencies.sort()
        
        return {
            'count': len(latencies),
            'min': min(latencies),
            'max': max(latencies),
            'avg': sum(latencies) / len(latencies),
            'p50': latencies[len(latencies) // 2],
            'p95': latencies[int(len(latencies) * 0.95)],
            'p99': latencies[int(len(latencies) * 0.99)],
        }
    
    def print_report(self) -> None:
        """Print metrics report to console"""
        print("\n" + "=" * 80)
        print("QAD METRICS REPORT")
        print("=" * 80)
        
        # Direct QAD latencies (from "Latency observed for" logs)
        qad_attach_stats = self.get_statistics(self.qad_attach_metrics)
        if qad_attach_stats:
            print("\nQAD ATTACH LATENCIES (from 'Latency observed for' logs)")
            print("-" * 40)
            self._print_stats(qad_attach_stats)
        
        qad_detach_stats = self.get_statistics(self.qad_detach_metrics)
        if qad_detach_stats:
            print("\nQAD DETACH LATENCIES (from 'Latency observed for' logs)")
            print("-" * 40)
            self._print_stats(qad_detach_stats)
        
        # Stage metrics
        stage_stats = self.get_statistics(self.stage_metrics)
        if stage_stats:
            print("\nNODE_STAGE_VOLUME LATENCIES (from CSI metrics)")
            print("-" * 40)
            self._print_stats(stage_stats)
        
        # Unstage metrics
        unstage_stats = self.get_statistics(self.unstage_metrics)
        if unstage_stats:
            print("\nNODE_UNSTAGE_VOLUME LATENCIES (from CSI metrics)")
            print("-" * 40)
            self._print_stats(unstage_stats)
        
        # By request type
        print("\n\nDETAILED BREAKDOWN BY REQUEST TYPE")
        print("-" * 40)
        for request_type in sorted(self.metrics.keys()):
            metrics_list = self.metrics[request_type]
            stats = self.get_statistics(metrics_list)
            if stats:
                print(f"\n{request_type}:")
                self._print_stats(stats)
        
        # Failed requests
        failed = [m for m in self.stage_metrics + self.unstage_metrics 
                  if m.get('result_code') != 'succeeded']
        if failed:
            print("\n\nFAILED REQUESTS")
            print("-" * 40)
            for m in failed:
                print(f"  {m['request']}: {m['result_code']} ({m['latency_seconds']}s)")
        
        print("\n" + "=" * 80 + "\n")
    
    @staticmethod
    def _print_stats(stats: Dict) -> None:
        """Print statistics in table format"""
        print(f"  Count: {stats['count']}")
        print(f"  Min:   {stats['min']:.3f}s")
        print(f"  P50:   {stats['p50']:.3f}s")
        print(f"  Avg:   {stats['avg']:.3f}s")
        print(f"  P95:   {stats['p95']:.3f}s")
        print(f"  P99:   {stats['p99']:.3f}s")
        print(f"  Max:   {stats['max']:.3f}s")
    
    def export_json(self, filepath: str) -> None:
        """Export metrics to JSON file"""
        stage_stats = self.get_statistics(self.stage_metrics)
        unstage_stats = self.get_statistics(self.unstage_metrics)
        qad_attach_stats = self.get_statistics(self.qad_attach_metrics)
        qad_detach_stats = self.get_statistics(self.qad_detach_metrics)
        
        export_data = {
            'timestamp': datetime.now().isoformat(),
            'qad_attach': {
                'statistics': qad_attach_stats,
                'raw_metrics': self.qad_attach_metrics
            },
            'qad_detach': {
                'statistics': qad_detach_stats,
                'raw_metrics': self.qad_detach_metrics
            },
            'stage_volume': {
                'statistics': stage_stats,
                'raw_metrics': self.stage_metrics
            },
            'unstage_volume': {
                'statistics': unstage_stats,
                'raw_metrics': self.unstage_metrics
            }
        }
        
        with open(filepath, 'w') as f:
            json.dump(export_data, f, indent=2)
        
        print(f"Metrics exported to {filepath}")


def main():
    """Main entry point"""
    if len(sys.argv) < 2:
        print("Usage: qad_metrics_extractor.py <log_file_or_dir> [output_json]")
        print("\nExamples:")
        print("  qad_metrics_extractor.py csi-logs-cycle-1-node1.log")
        print("  qad_metrics_extractor.py ./logs/ metrics.json")
        sys.exit(1)
    
    source = sys.argv[1]
    output_json = sys.argv[2] if len(sys.argv) > 2 else None
    
    extractor = QADMetricsExtractor()
    
    # Process files
    if os.path.isfile(source):
        print(f"Processing file: {source}")
        extractor.parse_log_file(source)
    elif os.path.isdir(source):
        print(f"Processing directory: {source}")
        for log_file in sorted(Path(source).glob("*.log")):
            print(f"  - {log_file.name}")
            extractor.parse_log_file(str(log_file))
    else:
        print(f"Error: {source} is not a valid file or directory")
        sys.exit(1)
    
    # Print report
    extractor.print_report()
    
    # Export JSON if requested
    if output_json:
        extractor.export_json(output_json)


if __name__ == "__main__":
    main()
