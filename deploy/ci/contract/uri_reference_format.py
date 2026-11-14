"""Makes `response_headers_conformance` validate `format: uri-reference` as RFC 3986 requires.

Measured with the pinned Schemathesis 4.33.2 (image digest in contract-tests.sh) and OpenAPI 3.0.x documents:
  1. values are validated with JSON Schema Draft 4, which has no `uri-reference` format, so the format is ignored;
  2. Schemathesis' documented extension point for such gaps, DRAFT4_SUPPLEMENTAL_FORMATS, is handed to the validators of
     request/response BODIES but NOT to the validators of response HEADERS (OpenApiResponseHeader.validator builds the
     validator without `formats`).
Two small, explicit steps close the gap, with no new dependency and no schema modification:
  a. register `uri-reference` in DRAFT4_SUPPLEMENTAL_FORMATS, backed by the URI parser already shipped in the image
     (`jsonschema_rs`, whose Draft 7 `uri-reference` format follows RFC 3986);
  b. make the header validator receive the same supplemental formats as every other validator.
Step b replaces one internal property of the pinned version. It is not a public API: location_regression.py proves in the
same image, through response_headers_conformance, that the structural check is really reached (including through the CLI,
via SCHEMATHESIS_HOOKS=uri_reference_format), and fails if a different Schemathesis build stops honoring it.

Importing the module installs both steps.
"""
import jsonschema_rs
from schemathesis.core import NOT_SET, NotSet
from schemathesis.core.jsonschema import DRAFT4_SUPPLEMENTAL_FORMATS, FANCY_REGEX_OPTIONS, supplemental_formats
from schemathesis.specs.openapi.adapter.responses import OpenApiResponseHeader

_RFC3986_URI_REFERENCE = jsonschema_rs.Draft7Validator({'format': 'uri-reference'}, validate_formats=True)


def is_uri_reference(value: object) -> bool:
    # Format checks only apply to strings, as for every other format.
    return not isinstance(value, str) or _RFC3986_URI_REFERENCE.is_valid(value)


def _header_validator(self) -> jsonschema_rs.Validator:
    """OpenApiResponseHeader.validator of 4.33.2, plus the supplemental formats."""
    if self._validator is NOT_SET:
        cls = self.adapter.jsonschema_validator_cls
        self._validator = cls(self.schema, validate_formats=True, pattern_options=FANCY_REGEX_OPTIONS,
                              formats=supplemental_formats(cls))
    assert not isinstance(self._validator, NotSet)
    return self._validator


DRAFT4_SUPPLEMENTAL_FORMATS['uri-reference'] = is_uri_reference
OpenApiResponseHeader.validator = property(_header_validator)
