#!/usr/bin/env python3

"""
QAD Test Report Generator
Generates professional HTML report from QAD test metrics
"""

import json
import sys
from datetime import datetime
from pathlib import Path


def generate_html_report(metrics_data, output_file="qad-report.html"):
    """Generate HTML report from metrics JSON"""
    
    # Try QAD metrics first, then fall back to stage/unstage
    qad_attach_stats = metrics_data.get('qad_attach', {}).get('statistics', {})
    qad_detach_stats = metrics_data.get('qad_detach', {}).get('statistics', {})
    stage_stats = metrics_data.get('stage_volume', {}).get('statistics', {})
    unstage_stats = metrics_data.get('unstage_volume', {}).get('statistics', {})
    
    html = f"""<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>QAD Performance Report</title>
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
        
        .status-badge {{
            display: inline-block;
            padding: 6px 12px;
            border-radius: 20px;
            font-size: 0.85em;
            font-weight: 600;
            margin-bottom: 20px;
        }}
        
        .status-good {{
            background: #d4edda;
            color: #155724;
        }}
        
        .status-warning {{
            background: #fff3cd;
            color: #856404;
        }}
        
        .status-critical {{
            background: #f8d7da;
            color: #721c24;
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
        
        .chart-placeholder {{
            background: #f8f9fa;
            padding: 30px;
            border-radius: 8px;
            text-align: center;
            color: #666;
            margin-bottom: 30px;
        }}
        
        .recommendations {{
            background: #e7f3ff;
            border-left: 5px solid #2196F3;
            padding: 20px;
            border-radius: 8px;
            margin-bottom: 30px;
        }}
        
        .recommendations h3 {{
            color: #1565c0;
            margin-bottom: 10px;
        }}
        
        .recommendations ul {{
            margin-left: 20px;
            color: #555;
            line-height: 1.8;
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
            <h1>🚀 QAD Performance Test Report</h1>
            <div class="timestamp">Generated: {datetime.now().strftime('%Y-%m-%d %H:%M:%S UTC')}</div>
        </header>
        
        <div class="metrics-grid">
            <div class="metric-card">
                <h2>📤 NODE_STAGE_VOLUME (Attach)</h2>
                {_render_stats(qad_attach_stats if qad_attach_stats else stage_stats)}
                {_status_badge((qad_attach_stats if qad_attach_stats else stage_stats).get('p95', 0))}
            </div>
            
            <div class="metric-card">
                <h2>📥 NODE_UNSTAGE_VOLUME (Detach)</h2>
                {_render_stats(qad_detach_stats if qad_detach_stats else unstage_stats)}
                {_status_badge((qad_detach_stats if qad_detach_stats else unstage_stats).get('p95', 0), "unstage")}
            </div>
        </div>
        
        <div class="comparison">
            <h3>⚡ Performance Summary</h3>
            <div class="comparison-item">
                <span>Attach Operations Completed:</span>
                <strong>{(qad_attach_stats if qad_attach_stats else stage_stats).get('count', 0)}</strong>
            </div>
            <div class="comparison-item">
                <span>Detach Operations Completed:</span>
                <strong>{(qad_detach_stats if qad_detach_stats else unstage_stats).get('count', 0)}</strong>
            </div>
            <div class="comparison-item">
                <span>Average Attach Time:</span>
                <strong>{(qad_attach_stats if qad_attach_stats else stage_stats).get('avg', 0):.3f}s</strong>
            </div>
            <div class="comparison-item">
                <span>Average Detach Time:</span>
                <strong>{(qad_detach_stats if qad_detach_stats else unstage_stats).get('avg', 0):.3f}s</strong>
            </div>
            <div class="comparison-item">
                <span>P95 Attach Latency:</span>
                <strong>{(qad_attach_stats if qad_attach_stats else stage_stats).get('p95', 0):.3f}s</strong>
            </div>
            <div class="comparison-item">
                <span>P95 Detach Latency:</span>
                <strong>{(qad_detach_stats if qad_detach_stats else unstage_stats).get('p95', 0):.3f}s</strong>
            </div>
        </div>
        
        <div class="recommendations">
            <h3>📋 Recommendations</h3>
            <ul>
                {_generate_recommendations(qad_attach_stats if qad_attach_stats else stage_stats, qad_detach_stats if qad_detach_stats else unstage_stats)}
            </ul>
        </div>
        
        <footer>
            <p>QAD Performance Testing Suite • Report Generated {datetime.now().strftime('%B %d, %Y at %H:%M:%S')} UTC</p>
            <p>For issues or questions, please refer to the QAD documentation</p>
        </footer>
    </div>
</body>
</html>"""
    
    with open(output_file, 'w') as f:
        f.write(html)
    
    print(f"Report saved to: {output_file}")
    return output_file


def _render_stats(stats):
    """Render statistics in HTML"""
    if not stats:
        return "<p>No data available</p>"
    
    return f"""
                <div class="metric-row">
                    <span class="metric-label">Count</span>
                    <span class="metric-value">{stats.get('count', 0)} ops</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">Min</span>
                    <span class="metric-value">{stats.get('min', 0):.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">P50 (Median)</span>
                    <span class="metric-value">{stats.get('p50', 0):.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">Average</span>
                    <span class="metric-value">{stats.get('avg', 0):.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">P95</span>
                    <span class="metric-value">{stats.get('p95', 0):.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">P99</span>
                    <span class="metric-value">{stats.get('p99', 0):.3f}s</span>
                </div>
                <div class="metric-row">
                    <span class="metric-label">Max</span>
                    <span class="metric-value">{stats.get('max', 0):.3f}s</span>
                </div>
    """


def _status_badge(p95_latency, operation="stage"):
    """Generate status badge based on latency"""
    if p95_latency < 3:
        return '<span class="status-badge status-good">✓ Excellent Performance</span>'
    elif p95_latency < 5:
        return '<span class="status-badge status-good">✓ Good Performance</span>'
    elif p95_latency < 10:
        return '<span class="status-badge status-warning">⚠ Acceptable - Monitor Closely</span>'
    else:
        return '<span class="status-badge status-critical">✗ High Latency - Investigate</span>'


def _generate_recommendations(stage_stats, unstage_stats):
    """Generate recommendations based on metrics"""
    recommendations = []
    
    if stage_stats.get('p95', 0) > 5:
        recommendations.append("<li>High P95 attach latency detected. Check Azure throttling and network latency.</li>")
    
    if stage_stats.get('max', 0) > 10:
        recommendations.append("<li>Significant outliers in attach operations. Investigate infrastructure health.</li>")
    
    if unstage_stats.get('p95', 0) > 3:
        recommendations.append("<li>Detach operations taking longer than expected. Check device cleanup and unmount performance.</li>")
    
    if stage_stats.get('count', 0) < 5:
        recommendations.append("<li>Limited samples collected. Run more test cycles for statistical significance.</li>")
    
    if not recommendations:
        recommendations.append("<li>✓ Performance metrics look healthy. No immediate action required.</li>")
        recommendations.append("<li>Continue monitoring and establish baseline metrics for future comparisons.</li>")
    
    return "\n                ".join(recommendations)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: python3 qad_report_generator.py <metrics.json> [output.html]")
        sys.exit(1)
    
    metrics_file = sys.argv[1]
    output_file = sys.argv[2] if len(sys.argv) > 2 else "qad-report.html"
    
    try:
        with open(metrics_file, 'r') as f:
            metrics = json.load(f)
        generate_html_report(metrics, output_file)
    except FileNotFoundError:
        print(f"Error: Metrics file not found: {metrics_file}")
        sys.exit(1)
    except json.JSONDecodeError:
        print(f"Error: Invalid JSON in {metrics_file}")
        sys.exit(1)
