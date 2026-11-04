# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

from datetime import UTC, datetime

import pytest

from comps.retrievers.utils.extractors.regex_extractor import RegexExtractor
from comps.retrievers.utils.normalizers.date_normalizer import DateNormalizer


def _ts(*args: int) -> int:
    return int(datetime(*args, tzinfo=UTC).timestamp())


@pytest.mark.parametrize(
    "language, query, expected_range",
    [
        (
            "en",
            "reports from Q1-Q3 2024",
            (_ts(2024, 1, 1), _ts(2024, 9, 30, 23, 59, 59)),
        ),
        (
            "pl",
            "raporty z Q2-Q4 2024",
            (_ts(2024, 4, 1), _ts(2024, 12, 31, 23, 59, 59)),
        ),
        (
            "pl",
            "raporty z I-III kwartał 2024",
            (_ts(2024, 1, 1), _ts(2024, 9, 30, 23, 59, 59)),
        ),
    ],
)
def test_quarter_range_spans_start_to_end_quarter(
    language: str, query: str, expected_range: tuple
) -> None:
    extractor = RegexExtractor(language=language)
    normalizer = DateNormalizer(
        date_context=extractor.get_date_context_keywords(),
        months=extractor.get_months(),
        time_units=extractor.get_time_units(),
        reference_time=datetime(2025, 6, 1, tzinfo=UTC),
    )
    extractions = [e for e in extractor.extract(query) if e.operator == "quarter_range"]
    assert len(extractions) == 1

    results = normalizer.normalize(extractions, query)

    assert len(results) == 1
    assert results[0].operator == "range"
    assert results[0].value == expected_range
