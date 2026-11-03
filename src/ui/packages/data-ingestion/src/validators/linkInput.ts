// Copyright (C) 2024-2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

import { containsNullCharacters } from "@intel-enterprise-rag-ui/input-validation";
import { z } from "zod";

import { LINK_ERROR_MESSAGES } from "@/utils/constants";

const validationSchema = z
  .string({ error: LINK_ERROR_MESSAGES.GENERIC })
  .min(1, LINK_ERROR_MESSAGES.GENERIC)
  .url(LINK_ERROR_MESSAGES.GENERIC)
  .regex(new RegExp("^https?://", "i"), LINK_ERROR_MESSAGES.GENERIC)
  .refine((v) => !/\s/.test(v.trim()), {
    message: LINK_ERROR_MESSAGES.WHITESPACE,
  })
  .refine((v) => !containsNullCharacters(v), {
    message: LINK_ERROR_MESSAGES.NULL_CHARACTERS,
  })
  .refine((v) => {
    try {
      const { hostname } = new URL(v);
      const isIPv6Literal = hostname.startsWith("[") && hostname.endsWith("]");
      const labels = hostname.split(".").filter(Boolean);
      const isIPv4Literal =
        labels.length === 4 && labels.every((label) => /^\d+$/.test(label));
      const hasPercentEncoding = hostname.includes("%");
      return (
        !isIPv6Literal &&
        !isIPv4Literal &&
        !hasPercentEncoding &&
        labels.length >= 2
      );
    } catch {
      return false;
    }
  }, LINK_ERROR_MESSAGES.INVALID_HOST);

export const validateLinkInput = async (value: string) =>
  await validationSchema.parseAsync(value);
