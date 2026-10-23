# Contributing

Start with a reproducible issue or a small pull request. Run `make check` and `make build` before submitting. Keep regression tests in the existing relevant file under `tests/`; test fixtures must be explicitly synthetic.

The core works without an AI account. Agent-specific behavior belongs in adapters and examples. Preserve original records, keep consumers independently authorized, and distinguish received messages, accepted webhooks, business results and platform acknowledgements.

Describe platform behavior you actually tested. Local fixtures and a fake transport do not establish real WeCom delivery. Do not include customer data, deployment credentials or host-specific paths in a contribution.

Code comments use Chinese; public API names remain English. Contributions are provided under the repository MIT license.
