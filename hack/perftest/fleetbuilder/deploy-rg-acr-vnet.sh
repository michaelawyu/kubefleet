#!/bin/bash
set -e

RESOURCE_GROUP_NAME=${RESOURCE_GROUP_NAME:?Environment variable RESOURCE_GROUP_NAME is not set}
LOCATION=${LOCATION:?Environment variable LOCATION is not set}
REGISTRY_NAME_WO_SUFFIX=${REGISTRY_NAME_WO_SUFFIX:?Environment variable REGISTRY_NAME_WO_SUFFIX is not set}
VNET_NAME=${VNET_NAME:?Environment variable VNET_NAME is not set}
CUSTOM_TAGS=${CUSTOM_TAGS:-perf_test=true}

# Create an Azure resource group.
echo "Creating resource group $RESOURCE_GROUP_NAME in location $LOCATION..."
az group create \
    -n "$RESOURCE_GROUP_NAME" \
    -l "$LOCATION" \
    --tags "$CUSTOM_TAGS"

# Create an Azure Container Registry.
echo "Creating Azure Container Registry $REGISTRY_NAME_WO_SUFFIX in resource group $RESOURCE_GROUP_NAME..."
az acr create \
    -n "$REGISTRY_NAME_WO_SUFFIX" \
    -g "$RESOURCE_GROUP_NAME" \
    -l "$LOCATION" \
    --sku Basic \
    --tags "$CUSTOM_TAGS"

# Create an Azure VNet for the host clusters.
echo "Creating VNet $VNET_NAME in resource group $RESOURCE_GROUP_NAME..."
az network vnet create \
    -g "$RESOURCE_GROUP_NAME" \
    -n "$VNET_NAME" \
    --location "$LOCATION" \
    --address-prefixes "10.0.0.0/8" \
    --subnet-name "default" \
    --subnet-prefixes "10.0.0.0/16" \
    --tags "$CUSTOM_TAGS"
