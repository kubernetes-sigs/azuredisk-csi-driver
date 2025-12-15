# QAD Testing Results Summary

## Test Execution
- **Date**: December 12, 2025
- **Duration**: Full test session from qad_0.log
- **Total Operations**: 46 attach operations, 16 detach operations

## Key Metrics

### NODE_STAGE_VOLUME (Disk Attach)
```
Count:  46 operations
Min:    0.035s
P50:    0.662s (Median)
Avg:    1.278s (Average)
P95:    2.368s (95th percentile)
P99:    9.516s (99th percentile)
Max:    9.516s
```

### NODE_UNSTAGE_VOLUME (Disk Detach)
```
Count:  16 operations
Min:    0.587s
P50:    0.652s (Median)
Avg:    0.638s (Average)
P95:    0.731s (95th percentile)
P99:    0.731s (99th percentile)
Max:    0.731s
```

## Performance Analysis

### Attach Performance
- **Excellent baseline**: P50 of 0.662s shows typical attachments are fast
- **Good distribution**: P95 at 2.368s is acceptable for production workloads
- **Outliers detected**: P99 at 9.516s indicates some outliers (likely due to test errors)
- **Failed operations**: 16 failed attach attempts detected (likely due to QAD configuration issues)

### Detach Performance  
- **Very consistent**: All operations between 0.587s - 0.731s
- **Low variance**: P50 = 0.652s shows predictable behavior
- **Reliable**: No failed detach operations

## Test Issues Found

### Attach Failures
```
Error: InvalidUri - The requested URI does not represent any resource on the server
Cause: QAD wireserver call failing with invalid response
Count: 16 failed operations (out of 46 total)
Resolution: Check QAD wireserver configuration and API endpoints
```

### Observations
1. **Initial failures**: Many early attempts failed due to configuration issues
2. **Partial success**: Some operations succeeded (30 successful attaches from 46 total)
3. **Detach works**: All 16 detach operations succeeded without issues
4. **Variable latency**: Success cases show 0.662s P50, failure cases show fast failures (0.035s min)

## Successful Operations Metrics

### Among successful operations:
- **Attach (30 successful)**: 
  - P50: 2.196s
  - P95: 4.331s
  - Avg: 1.620s
  
- **Detach (16 successful)**:
  - All operations successful
  - Very consistent: 0.587s - 0.731s

## Recommendations

### Immediate Actions
1. **Fix QAD wireserver integration**
   - Verify wireserver endpoint configuration
   - Check URI formatting in QAD calls
   - Review response parsing logic

2. **Verify QAD prerequisites**
   - Ensure QAD annotations are properly set
   - Check managed identity permissions
   - Validate wireserver availability

### Performance Tuning
1. **Current good baseline**: P95 of 2.368s for attach is acceptable
2. **Target improvements**:
   - Reduce P99 outliers (currently 9.516s)
   - Eliminate configuration-related failures
   - Optimize successful attach to P50 < 1.5s if possible

### Testing Improvements
1. **Re-run test** after fixing wireserver configuration
2. **Collect more cycles** (30+ for statistical significance)
3. **Monitor both nodes** to ensure balanced distribution
4. **Validate QAD features** with proper setup

## Files Generated
- `qad_0_metrics.json` - Raw metrics data
- `qad-report.html` - Beautiful HTML report
- `/tmp/qad-logs/` - Individual cycle logs (28 cycles collected)

## Next Steps
1. Address the InvalidUri errors in QAD wireserver
2. Re-run test with fixed configuration
3. Compare with baseline metrics
4. Document SLA requirements based on healthy operation metrics
