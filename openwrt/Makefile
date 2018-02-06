# SPDX-License-Identifier: GPLv2

include $(TOPDIR)/rules.mk

PKG_NAME:=pacoloco
PKG_VERSION:=0.1
#PKG_RELEASE=$(PKG_VERSION)
PKG_SOURCE_PROTO:=git
PKG_SOURCE_URL:=git://github.com/anatol/pacoloco
PKG_SOURCE_SUBDIR:=$(PKG_NAME)-$(PKG_VERSION)
PKG_SOURCE_VERSION:=2f60d32c503957eb6827b4811e2ef4f0247ec1f1
PKG_SOURCE:=$(PKG_NAME)-$(PKG_VERSION).tar.gz
PKG_MAINTAINER:=Anatol Pomozov <anatol.pomozov@gmail.com>

include $(INCLUDE_DIR)/package.mk

define Package/pacoloco/default
	SECTION:=net
	CATEGORY:=Network
	SUBMENU:=Web Servers/Proxies
	TITLE:=pacoloco - lightweight pacman caching service
endef

TARGET_CFLAGS += -DPACOLOCO_CONFIG_FILE=\\\"/etc/config/pacoloco\\\"

define Package/pacoloco
	$(Package/pacoloco/default)
	DEPENDS := +libc
endef

define Package/pacoloco/description
	Pacoloco is a lightweight proxy server for Linux Arch Pacman package manager.
	Pacoloco tracks all shared arch repositories in the current network and redirects
	pacman requests to a local machine if it has required file.
endef

define Package/pacoloco/build/compile
	$(MAKE) -C $(PKG_BUILD_DIR)
endef

define Package/pacoloco/install
	$(INSTALL_DIR) $(1)/usr/sbin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/pacoloco $(1)/usr/sbin/pacoloco
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/pacoloco.init $(1)/etc/init.d/pacoloco
endef

$(eval $(call BuildPackage,pacoloco))
