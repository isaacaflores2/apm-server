# checktrace

checktrace is tool to validate trace events and determine if a trace will be sampled for a given tail-based sampling configuration.

1. The tool first analyzes the trace events to identify the parent transaction and key fields required for sampling decision (parent service name, parent outcome, etc).
   - This can help provide a sanity check to make sure the trace is valid.
2. Then the tool runs each trace event through the tail-based sampling processor to determine if the trace will be sampled.
    - This step uses the provided policy configuration, so it can help ensure the policy is configured correctly and matches the intended behavior.

## Usage
Use the default configuration:
```bash
  go run . -i /path/to/traces.json
```

Use a custom configuration file:
```bash
  go run . -i /path/to/traces.json -c /path/to/checktrace-config.yml
```

## Input File
The input file expects a JSON array of indexed trace event documents. See `testdata/example-traces.json` for an example.
Indexed trace events can be searched using the following query:
```
GET traces-apm-default/_search
{
  "query": {
    "range": {
      "@timestamp": {
        "gte": "now-1h",
        "lt": "now"
      }
    }
  }
}
```

## Configuration
checktrace expects the same fields as the APM Server's tail-based sampling configuration.
### Default Configuration
```yaml
apm-server:
  host: "127.0.0.1:8200"
  sampling:
    tail:
      enabled: true
      interval: 1s
      policies:
        - sample_rate: 1.0
```

## Example Output
Example output when checking the testdata traces found in: `testdata/example-traces.json`. 

The output shows invalid traces that do not have a parent transaction as a result some fields are undefined/unknown and the trace is not sampled.
```
failed to get sampling decision for trace 1c95e26a659d1376964f7d876ea504db: key not found
failed to get sampling decision for trace 52ec96630993627093136132c83c8fd0: key not found
failed to get sampling decision for trace bd266412a8471473661077293d339270: key not found


TRACE_ID                          EVENT_TYPE   PARENT_TRANSACTIONS  CHILD_TRANSACTIONS  PARENT_SERVICE_NAME  PARENT_ENV  PARENT_OUTCOME  SAMPLED
0b8064454d96d2367037bef04a423eca  transaction  1                    2                   go-elastic-agent     unknown     success         true
1c95e26a659d1376964f7d876ea504db  undefined    0                    1                   unknown              unknown     unknown         false
52ec96630993627093136132c83c8fd0  undefined    0                    1                   unknown              unknown     unknown         false
bd266412a8471473661077293d339270  undefined    0                    2                   unknown              unknown     unknown         false
feabab44a0233185e1796fc2ce6b4fc7  transaction  1                    2                   go-elastic-agent     unknown     success         true
```
