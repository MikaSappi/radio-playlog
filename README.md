# Radio playlog that's nicer than most things
This thing is designed to help you automate the played tracks reporting. The data is stored in GCP, and the service itself runs in GCP. You may use the service for free when it's public.

## Current state
If you want to run this locally, configure the `configuration.json` for your use case. Set `use_gcp` to `false` for local use.

## How to use
The app receives a HTTP POST and writes it into a csv locally, or into BigQuery in GCP. Locally, it compresses the csv each months and you can do whatever you want with it. In GCP it will either mail it to you, or to the PRO's, or both, or a friend.

---
# API
To get the API key, you may [contact us](https://fremen.fi/consultation) for now, until a UI is ready for onboarding. Use of this application is free of charge at least for the first adopters.
## Metadata
The app accepts JSON-encoded information using the following schema:
```JSON
{
"artist": "string",
"title": "string"
}
```
## Authentication
Add a header to your HTTP request `X-API-Key: unicorns-are-fluffy-and-so-on`.

## Timestamp
Timestamp is written by the receiving service and represents the time it received the request. This timestamp may vary even 500 ms depending on several factors. Timestamps are usually within the "intended" second.

---

# Use with Liquidsoap
```Liquidsoap

# Define the URL and your API-key
playlog_url = "the.url.not"
playlog_key = "secrets"

def playlog_send(m) =
  title = m["title"]
  artist = m["artist"]

# Check if you have data to send, fail if any is empty
# (because you have to have both fields for reporting)

  if
    title != "" or artist != ""
  then

  # Don't break if the string contains `"` or `'` or `{}` and so on

    data = json.stringify({title=title, artist=artist})
    response =
      http.post(
        playlog_url,
        headers=[
          ("X-API-Key", playlog_key),
          ("Content-Type", "application/json")
        ],
        data=data
      )

      # Log the response code

    log(
      label="playlog",
      "POST status=#{response.status_code}"
    )
  end
end

# Call this with your final master source

def on_md_handler(s) =
  s.on_metadata(synchronous=false, playlog_send)
end

```
For example, if your main source is called `radio`, you would call this somewhere close to the master output:
```Liquidsoap
radio = fallback(
[
studios,
sources,
fb_source
])

...

on_md_handler(radio)

...

output.icecast(radio, ...)
```

## With other systems
Anything that can send a HTTP POST to a URL in the internet can use this application.
```bash
curl -X POST https://instance-url.not/ \
  -H "X-API-Key: secrets" \
  -H "Content-Type: application/json" \
  -d '{"title":"Test Track","artist":"Test Artist"}'
```

---
# Buzz
![kubernetes-native](https://img.shields.io/badge/kubernetes-native-1D9E75)
![cloud-agnostic](https://img.shields.io/badge/cloud-agnostic-378ADD)
![gitops-first](https://img.shields.io/badge/gitops-first-7F77DD)
![runs on holy water](https://img.shields.io/badge/runs_on-holy_water-378ADD)
![zero-trust by the developers](https://img.shields.io/badge/zero--trust-by_the_developers-BA7517)
![shift-left](https://img.shields.io/badge/shift-left-D85A30)
![cncf-aligned](https://img.shields.io/badge/cncf-aligned-639922)
![slsa-level-4](https://img.shields.io/badge/slsa-level_4-1D9E75)
![opentelemetry-native](https://img.shields.io/badge/opentelemetry-native-378ADD)
![wasm-portable](https://img.shields.io/badge/wasm-portable-7F77DD)
![ford focus](https://img.shields.io/badge/ford-focus-378ADD)
![finops-optimized](https://img.shields.io/badge/finops-optimized-D85A30)
![cell-based](https://img.shields.io/badge/cell-based-BA7517)
![devex-focused](https://img.shields.io/badge/devex-focused-639922)
![blessed](https://img.shields.io/badge/blessed-✝-1D9E75)
![AI-adjacent](https://img.shields.io/badge/AI-adjacent-7F77DD)
![tested in production](https://img.shields.io/badge/tested_in-production_(once)-D85A30)
![uptime: mostly](https://img.shields.io/badge/uptime-mostly-BA7517)
![GDPR-vibes compliant](https://img.shields.io/badge/GDPR--vibes-compliant-639922)
![backwards compatible: spiritually](https://img.shields.io/badge/backwards_compatible-spiritually-1D9E75)
![documentation: aspirational](https://img.shields.io/badge/documentation-aspirational-378ADD)
![bus factor: 1](https://img.shields.io/badge/bus_factor-1-D85A30)
![on-call: call a priest](https://img.shields.io/badge/on--call-call_a_priest-7F77DD)

The Radio Playlog Super 2000-X Professional is a Kubernetes-native, GitOps-first, zero-trust, event-driven, horizontally-scalable, observable-by-default, declarative-everything platform built for the post-cloud, pre-singularity, pre-burnout enterprise. Designed with a developer-experience-first, platform-engineering-enabled, internal-developer-platform-compatible, golden-path-compliant, vibes-driven architecture, Radio Playlog Super 2000-X Professional delivers production-grade infrastructure on day zero — not day one, not day two, day zero. We don't know what happens on day one.

Secure by design. Supply-chain-hardened, policy-as-code enforced, ambient-mesh-compatible, eBPF-accelerated, SBOM-attested, zero-CVE-aspirational security posture blessed by an ordained Kubernetes administrator. We shift left so hard we are now in the previous quarter. Zero-trust architecture. The developers are also not trusted.

Observable out of the box. OpenTelemetry-native traces, metrics, and logs with FinOps-optimized cardinality reduction, chaos-engineering-friendly fault injection hooks, and a single-pane-of-glass dashboard that requires four other dashboards to fully understand. Alerts fire in real time. We have not looked at them.

Infinitely scalable. Cell-based, serverless-first-with-stateful-fallback, multi-cloud-agnostic-but-tested-only-on-AWS architecture that scales to zero and beyond. Scales to zero particularly well. Eventual consistency guaranteed. The eventual part is load-bearing.

Platform engineering ready. Golden path compliant. IDP compatible. WebAssembly runtime portable. Runs on holy water and one medium-sized AWS bill. Works seamlessly with your existing service mesh, assuming your existing service mesh works seamlessly, which it does not.
