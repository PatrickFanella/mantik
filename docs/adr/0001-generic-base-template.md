# ADR-0001: Inherited template provenance

Status: historical; Mantik's application choices are in ADR-0002

Mantik was created from SUBCULT's
`project-template-go-worker-postgres-docker`, itself derived from the generic
`project-template`. The generic template separates stack-specific code from
shared issue forms, PR conventions, documentation structure, and verification.

Mantik retains those contribution conventions and GPL-3.0-only licensing.
It is an application repository; the generic template's stack-neutrality rule
does not govern Mantik's application architecture.
