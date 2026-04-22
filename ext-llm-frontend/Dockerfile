# Stage 1: Build
FROM node:20-alpine AS build
WORKDIR /app

# Copy package files
COPY package*.json ./

# Install dependencies including the specific native bindings for linux-musl
# This solves the "Cannot find native binding" error during build
RUN npm install

COPY . .
RUN npm run build

# Stage 2: Serve
FROM nginx:alpine
# Copy the custom nginx config
COPY nginx.conf /etc/nginx/conf.d/default.conf
# Copy the build output from Vite
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 8766
CMD ["nginx", "-g", "daemon off;"]