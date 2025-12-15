#!/usr/bin/env python3

"""
CSI Report Generator
Generate HTML report from CSI metrics JSON
"""

import json
from pathlib import Path
from datetime import datetime


def generate_csi_report():
    metrics_file = Path('/home/laandrea/go/src/github.com/azuredisk-csi-driver/test-qad/csi_metrics.json')
    
    with open(metrics_file, 'r') as f:
        metrics = json.load(f)
    
    stage_stats = metrics['csi_stage_volume']['statistics']
    unstage_stats = metrics['csi_unstage_volume']['statistics']
    
    html = f"""<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>CSI Metrics Report</title>
    <style>
        * {{
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }}
        
        body {{
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Roboto", "Oxygen", "Ubuntu", sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            padding: 20px;
            min-height: 100vh;
        }}
        
        .container {{
            max-width: 1200px;
            margin: 0 auto;
            background: white;
            border-radius: 12px;
            box-shadow: 0 20px 60px rgba(0,0,0,0.3);
            padding: 40px;
        }}
        
        header {{
            border-bottom: 3px solid #667eea;
            padding-bottom: 30px;
            margin-bottom: 30px;
        }}
        
        h1 {{
            color: #333;
            font-size: 2.5em;
            margin-bottom: 10px;
        }}
        
        .timestamp {{
            color: #666;
            font-size: 0.95em;
        }}
        
        .metrics-grid {{
            display: grid;
            grid-template-columns: 1fr 1fr;
            gap: 30px;
            margin-bottom: 40px;
        }}
        
        .metric-card {{
            background: #f8f9fa;
            border-left: 5px solid #667eea;
            padding: 25px;
            border-radius: 8px;
        }}
        
        .metric-card h2 {{
            color: #667eea;
            font-size: 1.3em;
            margin-bottom: 20px;
            border-bottom: 2px solid #e9ecef;
            padding-bottom: 10px;
        }}
        
        .metric-row {{
            display: flex;
            justify-content: space-between;
            padding: 12px 0;
            border-bottom: 1px solid #e9ecef;
        }}
        
        .metric-row:last-child {{
            border-bottom: none;
        }}
        
        .metric-label {{
            color: #666;
            font-weight: 500;
        }}
        
        .metric-value {{
            color: #333;
            font-weight: 600;
            font-family: "Monaco", "Courier New", monospace;
        }}
        
        .comparison {{
            background: linear-gradient(135deg, #667eea15 0%, #764ba215 100%);
            padding: 20px;
            border-radius: 8px;
            margin-bottom: 30px;
        }}
        
        .comparison h3 {{
            color: #333;
            margin-bottom: 15px;
        }}
        
        .comparison-item {{
            display: flex;
            justify-content: space-between;
            padding: 10px 0;
            font-size: 0.95em;
        }}
        
        .note {{
            background: #e7f3ff;
            border-left: 5px solid #2196F3;
            padding: 20px;
            border-radius: 8px;
            margin-bottom: 30px;
            color: #1565c0;
        }}
        
        footer {{
            text-align: center;
            color: #999;
            font-size: 0.85em;
            margin-top: 40px;
            padding-top: 20px;
            border-top: 1px solid #e9ecef;
        }}
        
        @media (max-width: 768px) {{
            .metrics-grid {{
                grid-template-columns: 1fr;
            }}
            h1 {{
                font-size: 1.8em;
            }}
            .container {{
                padding: 20px;
            }}
        }}
    </style>
</head>
<body>
    <div class="container">
        <header>
            <h1>📊 CSI Metrics Report</h1>
            <div class="timestamp">Generated: {datetime.now().strftime('%Y-%m-%d %H:%M:%S UTC')}</div>
        </header>
        
        <div class="note">
            <strong>Note:</strong> These metrics show the complete latency of CSI operations including disk attachment/detachment, formatting, mounting, and filesystem operations. QAD (Quick Attach-Detach) metrics show only the disk operation time.
        </div>
        
        <div class="metrics-grid">
            <div class="metric-card">
                <h2>📤 CSI Node Stage Volume</h2>
                <div class="metric-row">
                    <span class="metric-label">Count</span>
                    <span class="metric-value">{stage_stats.get('count', 0)} ops</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">Min</span>
                    <span class="metric-value">{stage_stats['min']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">P50 (Median)</span>
                    <span class="metric-value">{stage_stats['p50']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">Average</span>
                    <span class="metric-value">{stage_stats['avg']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">P95</span>
                    <span class="metric-value">{stage_stats['p95']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">P99</span>
                    <span class="metric-value">{stage_stats['p99']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">Max</span>
                    <span class="metric-value">{stage_stats['max']:.3f}s</span>
                </div>
            </div>
            
            <div class="metric-card">
                <h2>📥 CSI Node Unstage Volume</h2>
                <div class="metric-row">
                    <span class="metric-label">Count</span>
                    <span class="metric-value">{unstage_stats.get('count', 0)} ops</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">Min</span>
                    <span class="metric-value">{unstage_stats['min']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">P50 (Median)</span>
                    <span class="metric-value">{unstage_stats['p50']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">Average</span>
                    <span class="metric-value">{unstage_stats['avg']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">P95</span>
                    <span class="metric-value">{unstage_stats['p95']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">P99</span>
                    <span class="metric-value">{unstage_stats['p99']:.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">Max</span>
                    <span class="metric-value">{unstage_stats['max']:.3f}s</span>
                </div>
            </div>
        </div>
        
        <div class="comparison">
            <h3>⚡ CSI Operations Summary</h3>
            <div class="comparison-item">
                <span>Stage Volume Operations:</span>
                <strong>{stage_stats.get('count', 0)}</strong>
            </div>
            <div class="comparison-item">
                <span>Unstage Volume Operations:</span>
                <strong>{unstage_stats.get('count', 0)}</strong>
            </div>
            <div class="comparison-item">
                <span>Average Stage Time:</span>
                <strong>{stage_stats.get('avg', 0):.3f}s</strong>
            </div>
            <div class="comparison-item">
                <span>Average Unstage Time:</span>
                <strong>{unstage_stats.get('avg', 0):.3f}s</strong>
            </div>
        </div>
        
        <footer>
            <p>CSI Metrics Report • Azure Disk CSI Driver v1.34.0</p>
            <p>Generated: {datetime.now().strftime('%B %d, %Y at %H:%M:%S UTC')}</p>
        </footer>
    </div>
</body>
</html>"""
    
    report_file = Path('/home/laandrea/go/src/github.com/azuredisk-csi-driver/test-qad/csi_metrics_report.html')
    with open(report_file, 'w') as f:
        f.write(html)
    
    print(f"CSI report saved to: {report_file}")


if __name__ == '__main__':
    generate_csi_report()
