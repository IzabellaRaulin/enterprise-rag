// Copyright (C) 2024-2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

import { keycloakService } from "@intel-enterprise-rag-ui/auth";
import {
  selectIsChatSideMenuOpen,
  toggleChatSideMenu,
} from "@intel-enterprise-rag-ui/chat";
import {
  AppHeaderLeftSideContent as SharedAppHeaderLeftSideContent,
  AppHeaderRightSideContent as SharedAppHeaderRightSideContent,
} from "@intel-enterprise-rag-ui/layouts";
import { useLocation } from "react-router-dom";

import ViewSwitchButton from "@/components/ViewSwitchButton/ViewSwitchButton";
import { paths } from "@/config/paths";
import { useAppDispatch, useAppSelector } from "@/store/hooks";
import { resetStore } from "@/store/utils";
import { getChatQnAAppEnv } from "@/utils";

const viewSwitchButton = <ViewSwitchButton />;

const APP_NAME = "Intel® AI for Enterprise RAG";
const APP_VERSION =
  getChatQnAAppEnv("ERAG_VERSION") || import.meta.env.VITE_APP_VERSION;
const MAINTENANCE_MODE = getChatQnAAppEnv("MAINTENANCE_MODE");
const USER_GUIDE_URL =
  "https://github.com/intel/enterprise-rag/blob/main/docs/Intel_Chatbot_Toolkit_ChatQnA_User_Guide.pdf";

export const AppHeaderLeftSideContent = () => {
  const location = useLocation();
  const isChatRoute = location.pathname.startsWith(paths.chat);
  const isChatSideMenuOpen = useAppSelector(selectIsChatSideMenuOpen);
  const dispatch = useAppDispatch();

  return (
    <SharedAppHeaderLeftSideContent
      appName={APP_NAME}
      maintenanceMode={MAINTENANCE_MODE === "true"}
      isChatRoute={isChatRoute}
      isChatSideMenuOpen={isChatSideMenuOpen}
      onToggleChatSideMenu={() => dispatch(toggleChatSideMenu())}
    />
  );
};

export const AppHeaderRightSideContent = ({
  onNewChat,
}: {
  onNewChat?: () => void;
}) => {
  const location = useLocation();
  const isSpecificChatRoute =
    location.pathname.startsWith(paths.chat) &&
    location.pathname !== paths.chat;

  return (
    <SharedAppHeaderRightSideContent
      appName={APP_NAME}
      appVersion={APP_VERSION}
      userGuideUrl={USER_GUIDE_URL}
      username={keycloakService.getUsername()}
      onLogout={() => {
        resetStore();
        keycloakService.redirectToLogout();
      }}
      showNewChatButton={isSpecificChatRoute}
      onNewChat={onNewChat}
      renderViewSwitchButton={viewSwitchButton}
    />
  );
};
