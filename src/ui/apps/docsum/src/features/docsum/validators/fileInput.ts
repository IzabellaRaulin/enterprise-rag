// Copyright (C) 2024-2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

import {
  getFilenameInvalidCharactersMsg,
  getFileSizeWithinLimitMsg,
  getUnsupportedFileExtensionMsg,
  getUnsupportedFileMIMETypeMsg,
  individualFileSizeWithinLimit,
  isFileExtensionSupported,
  isMIMETypeSupported,
  noInvalidCharactersInFileName,
} from "@intel-enterprise-rag-ui/input-validation";
import { z } from "zod";

import { CLIENT_MAX_BODY_SIZE } from "@/config/api";
import {
  SUPPORTED_FILE_EXTENSIONS,
  SUPPORTED_FILES_MIME_TYPES,
} from "@/features/docsum/utils/fileInput";

const validationSchema = z
  .instanceof(File)
  .refine(isFileExtensionSupported(SUPPORTED_FILE_EXTENSIONS), {
    error: (issue) =>
      getUnsupportedFileExtensionMsg({ value: issue.input as File }),
  })
  .refine(isMIMETypeSupported(SUPPORTED_FILES_MIME_TYPES), {
    error: (issue) =>
      getUnsupportedFileMIMETypeMsg({ value: issue.input as File }),
  })
  .refine(noInvalidCharactersInFileName, {
    error: (issue) =>
      getFilenameInvalidCharactersMsg({ value: issue.input as File }),
  })
  .refine(individualFileSizeWithinLimit(CLIENT_MAX_BODY_SIZE), {
    error: (issue) =>
      getFileSizeWithinLimitMsg(CLIENT_MAX_BODY_SIZE)({
        value: issue.input as File,
      }),
  });

export const validateFileInput = async (file: File) => {
  await validationSchema.parseAsync(file);
};
