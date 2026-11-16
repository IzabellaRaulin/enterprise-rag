// Copyright (C) 2024-2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

import { keycloakService } from "@intel-enterprise-rag-ui/auth";
import {
  AppHeaderLeftSideContent as SharedAppHeaderLeftSideContent,
  AppHeaderRightSideContent as SharedAppHeaderRightSideContent,
} from "@intel-enterprise-rag-ui/layouts";

import ViewSwitchButton from "@/components/ViewSwitchButton/ViewSwitchButton";
import { resetStore } from "@/store/utils";
import { getDocSumAppEnv } from "@/utils";

const viewSwitchButton = <ViewSwitchButton />;

const APP_NAME = "Intel® Chatbot Toolkit";
const APP_VERSION =
  getDocSumAppEnv("ERAG_VERSION") || import.meta.env.VITE_APP_VERSION;
const USER_GUIDE_URL =
  "https://github.com/intel/enterprise-rag/blob/main/docs/Intel_Chatbot_Toolkit_DocSum_User_Guide.pdf";

export const AppHeaderLeftSideContent = () => (
  <SharedAppHeaderLeftSideContent appName="Document Summarization" />
);

export const AppHeaderRightSideContent = () => (
  <SharedAppHeaderRightSideContent
    appName={APP_NAME}
    appVersion={APP_VERSION}
    userGuideUrl={USER_GUIDE_URL}
    username={keycloakService.getUsername()}
    onLogout={() => {
      resetStore();
      keycloakService.redirectToLogout();
    }}
    renderViewSwitchButton={viewSwitchButton}
  />
);
